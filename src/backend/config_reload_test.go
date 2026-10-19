//go:build testing

package magitrickle

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/utils/intID"
	"magitrickle/utils/iptables"
	"magitrickle/utils/netfilterTools"
)

type reloadExecutable struct {
	*iptables.FakeIPTables
	reads  int
	failAt int
}

func (e *reloadExecutable) Save() ([]byte, error) {
	e.reads++
	if e.reads == e.failAt {
		return nil, errors.New("synthetic kernel reload failure")
	}
	return e.FakeIPTables.Save()
}

func TestReloadRollsBackSubscriptionsGroupsAndConfig(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig}
	a.publishConfigLocked()
	oldGroup := &models.Group{ID: intID.ID{1}, Name: "old group", Enable: false, Color: "#ffffff"}
	oldSub := &models.Subscription{ID: intID.ID{2}, Name: "old sub", Enable: false, URL: "https://old.example/list"}
	if err := a.AddGroup(oldGroup); err != nil {
		t.Fatal(err)
	}
	if err := a.AddSubscription(oldSub); err != nil {
		t.Fatal(err)
	}
	e := &reloadExecutable{FakeIPTables: iptables.NewFakeIPTables(iptables.ProtocolIPv4), failAt: 2}
	a.nfHelper = &netfilterTools.Helper{ChainPrefix: "MT_", IPTables4: iptables.NewIPTables(e)}
	a.enabled.Store(true)
	defer a.enabled.Store(false)
	filename := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("configVersion: 0.7.0\napp:\n  logLevel: debug\n  httpWeb:\n    allowedHosts: [router.example]\nsubscriptions:\n  - {id: '02000000', name: new sub, url: 'https://new.example/list', enable: false}\ngroups:\n  - {id: '01000000', name: new group, enable: false}\n")
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	previous := a.Config()
	if err := a.loadConfig(filename); err == nil {
		t.Fatal("kernel failure was ignored")
	}
	if !reflect.DeepEqual(previous, a.Config()) || a.subscriptions[0] != oldSub || a.userRuleSets[0].Model() != oldGroup {
		t.Fatal("failed reload changed working state")
	}
	if a.configDNSRules != nil {
		t.Fatal("failed transaction retained pending DNS rules")
	}
	if err := a.saveConfig(filename); err == nil {
		t.Fatal("background save would overwrite unapplied manual config")
	}
	if err := a.loadConfig(filename); err != nil {
		t.Fatal(err)
	}
	if a.subscriptions[0].Name != "new sub" || a.userRuleSets[0].Model().Name != "new group" || a.Config().LogLevel != "debug" || a.Config().HTTPWeb.AllowedHosts[0] != "router.example" {
		t.Fatal("valid retry did not commit all changes")
	}
	if err := a.saveConfig(filename); err != nil {
		t.Fatal("successful reload did not clear save guard:", err)
	}
}

func TestReloadRejectsStartupSettingsBeforeChangingState(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig}
	a.publishConfigLocked()
	a.enabled.Store(true)
	defer a.enabled.Store(false)
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(filename, []byte("configVersion: 0.7.0\napp:\n  httpWeb:\n    host: {port: 9999}\n  logLevel: debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := a.Config()
	if err := a.loadConfig(filename); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(previous, a.Config()) {
		t.Fatal("rejected network settings were published")
	}
}

func TestReloadValidatesAllowedHostsBeforeApplying(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(filename, []byte("configVersion: 0.7.0\napp:\n  httpWeb:\n    allowedHosts: ['*.example.com']\n  logLevel: debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.loadConfig(filename); err == nil {
		t.Fatal("wildcard Host accepted")
	}
	if a.Config().LogLevel != constant.DefaultAppConfig.LogLevel {
		t.Fatal("invalid host changed config")
	}
}
