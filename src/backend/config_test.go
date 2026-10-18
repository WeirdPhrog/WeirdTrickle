package magitrickle

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func TestShippedDefaultConfigsLoad(t *testing.T) {
	for _, platform := range []string{"common", "openwrt"} {
		t.Run(platform, func(t *testing.T) {
			filename := filepath.Join("..", "..", "files", platform, "var", "lib", "magitrickle", "config.yaml")
			loaded := &App{config: constant.DefaultAppConfig}
			if err := loaded.loadConfig(filename); err != nil {
				t.Fatalf("fresh installation cannot load %s: %v", filename, err)
			}
			if len(loaded.userRuleSets) == 0 {
				t.Fatal("shipped example group was not loaded")
			}
		})
	}
}

func TestConfigRoundTripAcrossForkVersions(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig}
	group := &models.Group{ID: intID.ID{1}, Name: "exception", Interface: "nwg1", Enable: true, Color: "#112233", Rules: []*models.Rule{{ID: intID.ID{2}, Type: models.RuleTypeDomain, Rule: "example.com", Enable: true}}}
	if err := a.AddGroup(group); err != nil {
		t.Fatal(err)
	}
	sub := &models.Subscription{ID: intID.ID{3}, Name: "ASN", URL: "https://example.com/asn", Interface: "nwg0", Enable: true, Interval: 3600, Rules: []*models.SubscriptionRule{{ID: intID.ID{4}, Type: models.RuleTypeSubnet, Rule: "192.0.2.0/24", Enable: false}}}
	if err := a.AddSubscription(sub); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := a.saveConfig(filename); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("configVersion: "+constant.ConfigVersion)) {
		t.Fatal("release version leaked into schema version")
	}
	for _, version := range []string{"0.7.0", "0.8.0", "2026.10.14.4"} {
		t.Run(version, func(t *testing.T) {
			input := bytes.Replace(data, []byte("configVersion: "+constant.ConfigVersion), []byte("configVersion: "+version), 1)
			if err := os.WriteFile(filename, input, 0600); err != nil {
				t.Fatal(err)
			}
			loaded := &App{config: constant.DefaultAppConfig}
			if err := loaded.loadConfig(filename); err != nil {
				t.Fatal(err)
			}
			if len(loaded.userRuleSets) != 1 || !reflect.DeepEqual(loaded.userRuleSets[0].Model(), group) {
				t.Fatal("group changed across restart")
			}
			if len(loaded.subscriptions) != 1 || !reflect.DeepEqual(loaded.subscriptions[0], sub) {
				t.Fatal("subscription changed across restart")
			}
			if err := loaded.saveConfig(filename); err != nil {
				t.Fatal(err)
			}
			reloaded := &App{}
			if err := reloaded.loadConfig(filename); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidConfigLeavesExistingGroupsIntact(t *testing.T) {
	a := &App{}
	group := &models.Group{ID: intID.ID{1}, Name: "keep"}
	if err := a.AddGroup(group); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	for _, data := range []string{
		"configVersion: 1.0.0\ngroups: []\n",
		"configVersion: 0.7.0\ngroups: [null]\n",
		"configVersion: 0.7.0\nsubscriptions: [null]\n",
		"configVersion: 0.7.0\ngroups:\n  - rules:\n      - {type: unsupported, rule: example.com, enable: true}\n",
		"configVersion: 0.7.0\nsubscriptions:\n  - rules:\n      - {type: unsupported, rule: example.com, enable: true}\n",
	} {
		if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.loadConfig(filename); err == nil {
			t.Fatal("invalid config accepted")
		}
		if len(a.userRuleSets) != 1 || a.userRuleSets[0].Model() != group {
			t.Fatal("invalid config destroyed current groups")
		}
	}
}

func TestConcurrentConfigSaveAndGroupEdits(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig}
	if err := a.AddGroup(&models.Group{ID: intID.ID{1}}); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				if err := a.UpdateGroup(&models.Group{ID: intID.ID{1}, Name: strings.Repeat("x", n*100)}); err != nil {
					t.Error(err)
					return
				}
				if err := a.saveConfig(filename); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := (&App{}).loadConfig(filename); err != nil {
		t.Fatal(err)
	}
	a.configLoadErr = errors.New("bad config")
	before, _ := os.ReadFile(filename)
	if err := a.saveConfig(filename); err == nil {
		t.Fatal("overwrote unreadable config")
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("config changed on failed save")
	}
}
