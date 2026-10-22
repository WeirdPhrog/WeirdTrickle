package magitrickle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"magitrickle/config"
	"magitrickle/constant"
	groupruntime "magitrickle/groups"
	"magitrickle/models"
	"magitrickle/subscriptions"

	"go.yaml.in/yaml/v2"
)

const cfgFolderLocation = constant.AppStateDir
const cfgFileLocation = cfgFolderLocation + "/config.yaml"

// Early WeirdTrickle releases wrote their calendar release version into the YAML.
// They use the same 0.x schema; accept them to recover existing installations.
var calendarConfigVersion = regexp.MustCompile(`^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}\.[0-9]+$`)

func applyIfSet[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func (a *App) LoadConfig() error {
	return a.loadConfig(cfgFileLocation)
}

func (a *App) loadConfig(filename string) (err error) {
	// Auto-refresh takes subscriptionSyncMu before saving; keep the same order.
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()
	a.configFileMu.Lock()
	defer a.configFileMu.Unlock()
	defer func() {
		a.stateMu.Lock()
		a.configLoadErr = err
		a.stateMu.Unlock()
	}()
	cfgFile, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}
	cfg := config.Config{}
	err = yaml.Unmarshal(cfgFile, &cfg)
	if err != nil {
		return fmt.Errorf("failed to unmarshal config file: %w", err)
	}

	if !strings.HasPrefix(cfg.ConfigVersion, "0.") && !calendarConfigVersion.MatchString(cfg.ConfigVersion) {
		return ErrConfigUnsupportedVersion
	}
	if cfg.App != nil && cfg.App.SubscriptionFetchInterface != nil {
		if err := subscriptions.ValidateFetchInterface(*cfg.App.SubscriptionFetchInterface); err != nil {
			return err
		}
	}
	if cfg.Groups != nil {
		if err := validateGroups(*cfg.Groups); err != nil {
			return err
		}
	}
	if cfg.Subscriptions != nil {
		ids := make(map[string]bool, len(*cfg.Subscriptions))
		for _, sub := range *cfg.Subscriptions {
			if sub == nil {
				return fmt.Errorf("null subscription in config")
			}
			if ids[sub.ID.String()] {
				return fmt.Errorf("duplicate subscription ID in config")
			}
			ids[sub.ID.String()] = true
			if err := subscriptions.ValidateRules(sub.Rules); err != nil {
				return err
			}
		}
	}

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.configSnapshot.Load() == nil {
		a.publishConfigLocked()
	}
	nextConfig := a.config

	if cfg.App != nil {
		if cfg.App.HTTPWeb != nil {
			applyIfSet(&nextConfig.HTTPWeb.Enabled, cfg.App.HTTPWeb.Enabled)
			applyIfSet(&nextConfig.HTTPWeb.Skin, cfg.App.HTTPWeb.Skin)
			applyIfSet(&nextConfig.HTTPWeb.AllowedHosts, cfg.App.HTTPWeb.AllowedHosts)
			if cfg.App.HTTPWeb.Host != nil {
				applyIfSet(&nextConfig.HTTPWeb.Host.Address, cfg.App.HTTPWeb.Host.Address)
				applyIfSet(&nextConfig.HTTPWeb.Host.Port, cfg.App.HTTPWeb.Host.Port)
			}
			if cfg.App.HTTPWeb.Auth != nil {
				applyIfSet(&nextConfig.HTTPWeb.Auth.Enabled, cfg.App.HTTPWeb.Auth.Enabled)
			}
		}

		if cfg.App.DNSProxy != nil {
			if cfg.App.DNSProxy.Upstream != nil {
				applyIfSet(&nextConfig.DNSProxy.Upstream.Address, cfg.App.DNSProxy.Upstream.Address)
				applyIfSet(&nextConfig.DNSProxy.Upstream.Port, cfg.App.DNSProxy.Upstream.Port)
			}
			if cfg.App.DNSProxy.Host != nil {
				applyIfSet(&nextConfig.DNSProxy.Host.Address, cfg.App.DNSProxy.Host.Address)
				applyIfSet(&nextConfig.DNSProxy.Host.Port, cfg.App.DNSProxy.Host.Port)
			}
			applyIfSet(&nextConfig.DNSProxy.DisableRemap53, cfg.App.DNSProxy.DisableRemap53)
			applyIfSet(&nextConfig.DNSProxy.DisableFakePTR, cfg.App.DNSProxy.DisableFakePTR)
			applyIfSet(&nextConfig.DNSProxy.DisableDropAAAA, cfg.App.DNSProxy.DisableDropAAAA)
			applyIfSet(&nextConfig.DNSProxy.MaxIdleConns, cfg.App.DNSProxy.MaxIdleConns)
			applyIfSet(&nextConfig.DNSProxy.MaxConcurrent, cfg.App.DNSProxy.MaxConcurrent)
			if cfg.App.DNSProxy.Timeout != nil {
				// Preserve legacy configs that stored a bare millisecond count.
				t := *cfg.App.DNSProxy.Timeout
				if t < time.Millisecond {
					t *= time.Millisecond
				}
				nextConfig.DNSProxy.Timeout = t
			}
		}

		if cfg.App.Netfilter != nil {
			if cfg.App.Netfilter.IPTables != nil {
				applyIfSet(&nextConfig.Netfilter.IPTables.ChainPrefix, cfg.App.Netfilter.IPTables.ChainPrefix)
			}
			if cfg.App.Netfilter.IPSet != nil {
				applyIfSet(&nextConfig.Netfilter.IPSet.TablePrefix, cfg.App.Netfilter.IPSet.TablePrefix)
				if cfg.App.Netfilter.IPSet.AdditionalTTL != nil {
					// Preserve legacy configs that stored a bare second count.
					t := *cfg.App.Netfilter.IPSet.AdditionalTTL
					if t < time.Second {
						t *= time.Second
					}
					nextConfig.Netfilter.IPSet.AdditionalTTL = t
				}
			}
			applyIfSet(&nextConfig.Netfilter.DisableIPv4, cfg.App.Netfilter.DisableIPv4)
			applyIfSet(&nextConfig.Netfilter.DisableIPv6, cfg.App.Netfilter.DisableIPv6)
			applyIfSet(&nextConfig.Netfilter.StartMarkTableIndex, cfg.App.Netfilter.StartMarkTableIndex)
		}

		applyIfSet(&nextConfig.Link, cfg.App.Link)
		applyIfSet(&nextConfig.ShowAllInterfaces, cfg.App.ShowAllInterfaces)
		applyIfSet(&nextConfig.LogLevel, cfg.App.LogLevel)
		applyIfSet(&nextConfig.SubscriptionFetchInterface, cfg.App.SubscriptionFetchInterface)
	}
	for _, host := range nextConfig.HTTPWeb.AllowedHosts {
		if _, err := models.NormalizeHTTPHost(host); err != nil {
			return err
		}
	}
	nextConfig.Link = slices.Clone(nextConfig.Link)
	nextConfig.HTTPWeb.AllowedHosts = slices.Clone(nextConfig.HTTPWeb.AllowedHosts)
	if a.enabled.Load() && configNeedsRestart(a.config, nextConfig) {
		return fmt.Errorf("network/listener settings changed; restart the service to apply this config")
	}

	// Keep all proposed domains in the DNS prefilter throughout BOTH updates.
	// Nested group/subscription reconciliation must not drop the other class.
	if cfg.Groups != nil {
		a.configDNSRules = groupruntime.BuildRuntimeRuleSets(*cfg.Groups)
		for _, group := range *cfg.Groups {
			group.Color = models.NormalizeGroupColor(group.Color)
		}
	}
	if cfg.Subscriptions != nil {
		a.configDNSRules = append(a.configDNSRules, subscriptions.BuildRuntimeRuleSets(*cfg.Subscriptions)...)
	}
	a.beginConfigDNSRulesLocked(a.configDNSRules)
	defer func() {
		a.configDNSRules = nil
		a.publishDNSRulesLocked(nil)
	}()

	previousSubscriptions := a.subscriptions
	a.subscriptions = nil
	if cfg.Subscriptions != nil {
		a.subscriptions = slices.Clone(*cfg.Subscriptions)
	}
	restoreSubscriptions := func(cause error) error {
		a.subscriptions = previousSubscriptions
		if rollbackErr := a.syncSubscriptionRuleSetsLocked(); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("failed to restore subscriptions after config reload: %w", rollbackErr))
		}
		return cause
	}
	// Prepare broad subscriptions before groups can be removed. Group priority
	// remains enforced by the existing routing tiers during the transaction.
	if err := a.syncSubscriptionRuleSetsLocked(); err != nil {
		return restoreSubscriptions(err)
	}
	if cfg.Groups != nil {
		if err := a.replaceGroupsLocked(*cfg.Groups); err != nil {
			return restoreSubscriptions(err)
		}
	}
	// Configuration readers see the new snapshot only after routing succeeded.
	a.config = nextConfig
	a.publishConfigLocked()
	a.setupLogging()
	return nil
}

func configNeedsRestart(previous, next models.AppConfig) bool {
	startupOnly := func(c models.AppConfig) models.AppConfig {
		c.HTTPWeb.Auth.Enabled = false
		c.HTTPWeb.Skin = ""
		c.HTTPWeb.AllowedHosts = nil
		c.DNSProxy.DisableFakePTR = false
		c.DNSProxy.DisableDropAAAA = false
		c.Netfilter.IPSet.AdditionalTTL = 0
		c.ShowAllInterfaces = false
		c.LogLevel = ""
		c.SubscriptionFetchInterface = ""
		return c
	}
	return !reflect.DeepEqual(startupOnly(previous), startupOnly(next))
}

func (a *App) SaveConfig() error {
	return a.saveConfig(cfgFileLocation)
}

func (a *App) saveConfig(filename string) error {
	a.configFileMu.Lock()
	defer a.configFileMu.Unlock()
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.saveConfigLocked(filename)
}

// Caller holds configFileMu and stateMu (read or write).
func (a *App) saveConfigLocked(filename string) error {
	if a.configLoadErr != nil {
		return fmt.Errorf("refusing to overwrite an unreadable config: %w", a.configLoadErr)
	}

	groups := make([]*models.Group, 0, len(a.userRuleSets))
	for _, rs := range a.userRuleSets {
		if gm := rs.Model(); gm != nil {
			groups = append(groups, gm)
		}
	}

	cfg := config.Config{
		ConfigVersion: constant.ConfigVersion,
		App: &config.App{
			HTTPWeb: &config.HTTPWeb{
				Enabled: &a.config.HTTPWeb.Enabled,
				Auth: &config.Auth{
					Enabled: &a.config.HTTPWeb.Auth.Enabled,
				},
				Host: &config.HTTPWebServer{
					Address: &a.config.HTTPWeb.Host.Address,
					Port:    &a.config.HTTPWeb.Host.Port,
				},
				Skin: &a.config.HTTPWeb.Skin,
			},
			DNSProxy: &config.DNSProxy{
				Host: &config.DNSProxyServer{
					Address: &a.config.DNSProxy.Host.Address,
					Port:    &a.config.DNSProxy.Host.Port,
				},
				Upstream: &config.DNSProxyServer{
					Address: &a.config.DNSProxy.Upstream.Address,
					Port:    &a.config.DNSProxy.Upstream.Port,
				},
				DisableRemap53:  &a.config.DNSProxy.DisableRemap53,
				DisableFakePTR:  &a.config.DNSProxy.DisableFakePTR,
				DisableDropAAAA: &a.config.DNSProxy.DisableDropAAAA,
				MaxIdleConns:    &a.config.DNSProxy.MaxIdleConns,
				MaxConcurrent:   &a.config.DNSProxy.MaxConcurrent,
				Timeout:         &a.config.DNSProxy.Timeout,
			},
			Netfilter: &config.Netfilter{
				IPTables: &config.IPTables{
					ChainPrefix: &a.config.Netfilter.IPTables.ChainPrefix,
				},
				IPSet: &config.IPSet{
					TablePrefix:   &a.config.Netfilter.IPSet.TablePrefix,
					AdditionalTTL: &a.config.Netfilter.IPSet.AdditionalTTL,
				},
				DisableIPv4:         &a.config.Netfilter.DisableIPv4,
				DisableIPv6:         &a.config.Netfilter.DisableIPv6,
				StartMarkTableIndex: &a.config.Netfilter.StartMarkTableIndex,
			},
			Link:                       &a.config.Link,
			ShowAllInterfaces:          &a.config.ShowAllInterfaces,
			LogLevel:                   &a.config.LogLevel,
			SubscriptionFetchInterface: &a.config.SubscriptionFetchInterface,
		},
		Groups:        &groups,
		Subscriptions: &a.subscriptions,
	}
	if len(a.config.HTTPWeb.AllowedHosts) > 0 {
		cfg.App.HTTPWeb.AllowedHosts = &a.config.HTTPWeb.AllowedHosts
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return fmt.Errorf("failed to create config folder: %w", err)
	}
	if err := writeConfigAtomically(filename, out); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

func writeConfigAtomically(filename string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(filename), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}
