package magitrickle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"magitrickle/config"
	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/subscriptions"

	"github.com/dlclark/regexp2"
	"go.yaml.in/yaml/v2"
)

var colorRegExp = regexp2.MustCompile(`^#[0-9a-f]{6}$`, regexp2.IgnoreCase)

const cfgFolderLocation = constant.AppStateDir
const cfgFileLocation = cfgFolderLocation + "/config.yaml"

// Early fork releases wrote their calendar release version into the YAML.
// They use the same 0.x schema; accept them to recover existing installations.
var calendarConfigVersion = regexp.MustCompile(`^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}\.[0-9]+$`)

func applyIfSet[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func (a *App) LoadConfig() error {
	err := a.loadConfig(cfgFileLocation)
	a.stateMu.Lock()
	a.configLoadErr = err
	a.stateMu.Unlock()
	return err
}

func (a *App) loadConfig(filename string) error {
	a.configFileMu.Lock()
	cfgFile, err := os.ReadFile(filename)
	a.configFileMu.Unlock()
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
		for _, sub := range *cfg.Subscriptions {
			if sub == nil {
				return fmt.Errorf("null subscription in config")
			}
			for _, rule := range sub.Rules {
				if rule == nil {
					return fmt.Errorf("null subscription rule in config")
				}
			}
		}
	}

	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	if cfg.App != nil {
		if cfg.App.HTTPWeb != nil {
			applyIfSet(&a.config.HTTPWeb.Enabled, cfg.App.HTTPWeb.Enabled)
			applyIfSet(&a.config.HTTPWeb.Skin, cfg.App.HTTPWeb.Skin)
			if cfg.App.HTTPWeb.Host != nil {
				applyIfSet(&a.config.HTTPWeb.Host.Address, cfg.App.HTTPWeb.Host.Address)
				applyIfSet(&a.config.HTTPWeb.Host.Port, cfg.App.HTTPWeb.Host.Port)
			}
			if cfg.App.HTTPWeb.Auth != nil {
				applyIfSet(&a.config.HTTPWeb.Auth.Enabled, cfg.App.HTTPWeb.Auth.Enabled)
			}
		}

		if cfg.App.DNSProxy != nil {
			if cfg.App.DNSProxy.Upstream != nil {
				applyIfSet(&a.config.DNSProxy.Upstream.Address, cfg.App.DNSProxy.Upstream.Address)
				applyIfSet(&a.config.DNSProxy.Upstream.Port, cfg.App.DNSProxy.Upstream.Port)
			}
			if cfg.App.DNSProxy.Host != nil {
				applyIfSet(&a.config.DNSProxy.Host.Address, cfg.App.DNSProxy.Host.Address)
				applyIfSet(&a.config.DNSProxy.Host.Port, cfg.App.DNSProxy.Host.Port)
			}
			applyIfSet(&a.config.DNSProxy.DisableRemap53, cfg.App.DNSProxy.DisableRemap53)
			applyIfSet(&a.config.DNSProxy.DisableFakePTR, cfg.App.DNSProxy.DisableFakePTR)
			applyIfSet(&a.config.DNSProxy.DisableDropAAAA, cfg.App.DNSProxy.DisableDropAAAA)
			applyIfSet(&a.config.DNSProxy.MaxIdleConns, cfg.App.DNSProxy.MaxIdleConns)
			applyIfSet(&a.config.DNSProxy.MaxConcurrent, cfg.App.DNSProxy.MaxConcurrent)
			if cfg.App.DNSProxy.Timeout != nil {
				// Preserve legacy configs that stored a bare millisecond count.
				t := *cfg.App.DNSProxy.Timeout
				if t < time.Millisecond {
					t *= time.Millisecond
				}
				a.config.DNSProxy.Timeout = t
			}
		}

		if cfg.App.Netfilter != nil {
			if cfg.App.Netfilter.IPTables != nil {
				applyIfSet(&a.config.Netfilter.IPTables.ChainPrefix, cfg.App.Netfilter.IPTables.ChainPrefix)
			}
			if cfg.App.Netfilter.IPSet != nil {
				applyIfSet(&a.config.Netfilter.IPSet.TablePrefix, cfg.App.Netfilter.IPSet.TablePrefix)
				if cfg.App.Netfilter.IPSet.AdditionalTTL != nil {
					// Preserve legacy configs that stored a bare second count.
					t := *cfg.App.Netfilter.IPSet.AdditionalTTL
					if t < time.Second {
						t *= time.Second
					}
					a.config.Netfilter.IPSet.AdditionalTTL = t
				}
			}
			applyIfSet(&a.config.Netfilter.DisableIPv4, cfg.App.Netfilter.DisableIPv4)
			applyIfSet(&a.config.Netfilter.DisableIPv6, cfg.App.Netfilter.DisableIPv6)
			applyIfSet(&a.config.Netfilter.StartMarkTableIndex, cfg.App.Netfilter.StartMarkTableIndex)
		}

		applyIfSet(&a.config.Link, cfg.App.Link)
		applyIfSet(&a.config.ShowAllInterfaces, cfg.App.ShowAllInterfaces)
		applyIfSet(&a.config.LogLevel, cfg.App.LogLevel)
		applyIfSet(&a.config.SubscriptionFetchInterface, cfg.App.SubscriptionFetchInterface)
	}
	a.publishConfigLocked()

	if cfg.Groups != nil {
		for _, group := range *cfg.Groups {
			if match, _ := colorRegExp.MatchString(group.Color); !match {
				group.Color = "#ffffff"
			} else {
				group.Color = strings.ToLower(group.Color)
			}
		}
		if err := a.replaceGroupsLocked(*cfg.Groups); err != nil {
			return err
		}
	}

	if cfg.Subscriptions != nil {
		a.subscriptions = append(a.subscriptions[:0], *cfg.Subscriptions...)
	} else {
		a.subscriptions = a.subscriptions[:0]
	}

	return a.syncSubscriptionRuleSetsLocked()
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
