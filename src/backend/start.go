package magitrickle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"magitrickle/api"
	"magitrickle/utils/dnsMITMProxy"
	"magitrickle/utils/iptables"
	"magitrickle/utils/netfilterTools"
	"magitrickle/utils/recordsCache"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/vishvananda/netlink"
)

// Start запускает приложение (ядро)
func (a *App) Start(ctx context.Context) (err error) {
	a.stateMu.RLock()
	loadErr := a.configLoadErr
	a.publishConfigLocked()
	a.publishDNSRulesLocked(nil)
	a.stateMu.RUnlock()
	if loadErr != nil {
		return fmt.Errorf("cannot start with unreadable config: %w", loadErr)
	}
	if !a.enabled.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer a.enabled.Store(false)

	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic: %v\n%s\n", r, debug.Stack())
			err = errors.New(fmt.Sprintf("panic: %v", r))
		}
	}()

	a.setupLogging()
	newCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.dnsMITM = dnsMITMProxy.NewDNSMITMProxy(
		a.Config().DNSProxy.Upstream.HostPort(),
		a.Config().DNSProxy.MaxIdleConns,
		a.Config().DNSProxy.MaxConcurrent,
		a.Config().DNSProxy.Timeout,
	)
	a.dnsMITM.RequestHook = a.dnsRequestHook
	a.dnsMITM.ResponseHook = a.dnsResponseHook
	defer func() {
		if a.dnsMITM != nil {
			_ = a.dnsMITM.Close()
		}
	}()

	a.recordsCache = recordsCache.New()
	a.recordsCache.StartCleanup(newCtx, 30*time.Second)

	nfh, err := netfilterTools.New(a.Config().Netfilter.IPTables.ChainPrefix, a.Config().Netfilter.IPSet.TablePrefix, a.Config().Netfilter.DisableIPv4, a.Config().Netfilter.DisableIPv6, a.Config().Netfilter.StartMarkTableIndex)
	if err != nil {
		return fmt.Errorf("netfilter helper init fail: %w", err)
	}
	a.nfHelper = nfh

	for _, ipt := range []*iptables.IPTables{a.nfHelper.IPTables4, a.nfHelper.IPTables6} {
		if ipt == nil {
			continue
		}
		ipt.RegisterChainPatch("filter", "FORWARD")
		ipt.RegisterChainPatch("mangle", "PREROUTING")
		ipt.RegisterChainPatch("nat", "PREROUTING")
		ipt.RegisterChainPatch("nat", "POSTROUTING")
	}

	if err := a.nfHelper.CleanIPTables(); err != nil {
		return fmt.Errorf("failed to clear iptables: %w", err)
	}
	defer func() { _ = a.nfHelper.CleanIPTables() }()
	if err := a.nfHelper.InitRouting(); err != nil {
		return err
	}

	links := &networkSubscription[netlink.LinkUpdate]{subscribe: subscribeLinkUpdates, name: "links"}
	links.connect()
	defer links.close()
	addresses := &networkSubscription[netlink.AddrUpdate]{subscribe: subscribeAddrUpdates, name: "addresses"}
	addresses.connect()
	defer addresses.close()

	errChan := make(chan error, 4)

	if !a.Config().DNSProxy.DisableRemap53 {
		interfaceAddrs, err := a.lanAddresses(false)
		if err != nil {
			return err
		}
		a.dnsOverrider = a.nfHelper.PortRemap("DNSOR", 53, a.Config().DNSProxy.Host.Port, interfaceAddrs)
		if err := a.dnsOverrider.Enable(); err != nil {
			return fmt.Errorf("failed to override DNS: %v", err)
		}
		defer func() {
			_ = a.dnsOverrider.Disable()
		}()
	}

	// Also release already enabled rule sets if a later one fails at startup.
	defer func() {
		for _, group := range a.ruleSetSnapshot() {
			_ = group.Disable()
		}
	}()
	for _, group := range a.ruleSetSnapshot() {
		if err := group.Enable(); err != nil {
			return fmt.Errorf("failed to enable group: %w", err)
		}
	}
	// Publish the API only after every rule set is ready.
	httpServer, err := api.SetupHTTP(a, errChan)
	if err != nil {
		return fmt.Errorf("setup http fail: %w", err)
	}
	if httpServer != nil {
		defer httpServer.Close()
	}
	unixServer, err := api.SetupUnixSocket(a, errChan)
	if err != nil {
		return fmt.Errorf("setup unix socket fail: %w", err)
	}
	defer unixServer.Close()
	a.startDNSListeners(newCtx, errChan)
	autoDone := make(chan struct{})
	go func() { defer close(autoDone); a.StartSubscriptionAutoUpdate(newCtx) }()
	defer func() {
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if httpServer != nil {
			_ = httpServer.Shutdown(shutdownCtx)
		}
		_ = unixServer.Shutdown(shutdownCtx)
		<-autoDone
	}()

	restoreTicker := time.NewTicker(time.Minute)
	defer restoreTicker.Stop()
	var networkRepair networkRetry
	defer networkRepair.clear()
	recordNetworkError := func(err error) {
		if err != nil {
			log.Error().Err(err).Msg("network state repair failed; retaining routing and retrying")
			networkRepair.fail()
		}
	}
	repairNetwork := func() {
		if err := a.resyncNetworkState(); err != nil {
			recordNetworkError(err)
		} else {
			networkRepair.clear()
		}
	}
	for {
		select {
		case event, ok := <-links.updates:
			if !ok {
				links.lost()
				continue
			}
			links.retry.clear()
			recordNetworkError(errors.Join(a.refreshDNSRemap(), a.handleLink(event)))
		case event, ok := <-addresses.updates:
			if !ok {
				addresses.lost()
				continue
			}
			addresses.retry.clear()
			// Both additions and removals can change the router's DNS addresses.
			recordNetworkError(errors.Join(a.refreshDNSRemap(), a.handleAddr(event)))
		case <-links.retry.ready:
			links.retry.fired()
			if links.connect() {
				repairNetwork()
			}
		case <-addresses.retry.ready:
			addresses.retry.fired()
			if addresses.connect() {
				repairNetwork()
			}
		case <-networkRepair.ready:
			networkRepair.fired()
			repairNetwork()
		case err := <-errChan:
			return err
		case <-restoreTicker.C:
			if err := a.retryRoutingRestores(time.Now()); err != nil {
				log.Error().Err(err).Msg("failed to restore suspended routing; will retry")
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *App) ForceCommitIPTables() error {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.nfHelper == nil {
		return nil
	}

	if a.nfHelper.IPTables4 != nil {
		err := a.nfHelper.IPTables4.Commit()
		if err != nil {
			return fmt.Errorf("failed to commit iptables rules: %w", err)
		}
	}

	if a.nfHelper.IPTables6 != nil {
		err := a.nfHelper.IPTables6.Commit()
		if err != nil {
			return fmt.Errorf("failed to commit iptables rules: %w", err)
		}
	}

	return nil
}

func (a *App) setupLogging() {
	switch a.Config().LogLevel {
	case "trace":
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	case "fatal":
		zerolog.SetGlobalLevel(zerolog.FatalLevel)
	case "panic":
		zerolog.SetGlobalLevel(zerolog.PanicLevel)
	case "nolevel":
		zerolog.SetGlobalLevel(zerolog.NoLevel)
	case "disabled":
		zerolog.SetGlobalLevel(zerolog.Disabled)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}
