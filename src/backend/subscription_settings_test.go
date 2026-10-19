package magitrickle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"magitrickle/models"
	"magitrickle/utils/intID"
)

func TestSubscriptionFetchSettingsPersistence(t *testing.T) {
	a := &App{}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	for _, name := range []string{"nwg1", ""} {
		if err := a.setSubscriptionFetchInterface(name, filename); err != nil {
			t.Fatal(err)
		}
		loaded := &App{}
		if err := loaded.loadConfig(filename); err != nil {
			t.Fatal(err)
		}
		if loaded.Config().SubscriptionFetchInterface != name {
			t.Fatal("setting lost across restart")
		}
	}
	before, _ := os.ReadFile(filename)
	if err := a.setSubscriptionFetchInterface("bad interface", filename); err == nil {
		t.Fatal("accepted invalid interface")
	}
	after, _ := os.ReadFile(filename)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid setting changed config")
	}
	// Rename over a directory always fails, even when tests run as root.
	if err := a.setSubscriptionFetchInterface("nwg2", t.TempDir()); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	if a.Config().SubscriptionFetchInterface != "" {
		t.Fatal("failed save changed active setting")
	}
	if err := os.WriteFile(filename, []byte("configVersion: 0.7.0\napp: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := &App{}
	if err := legacy.loadConfig(filename); err != nil {
		t.Fatal(err)
	}
	if legacy.Config().SubscriptionFetchInterface != "" {
		t.Fatal("legacy config lost system default")
	}
}

func TestConcurrentSubscriptionFetchSettings(t *testing.T) {
	a := &App{}
	filename := filepath.Join(t.TempDir(), "config.yaml")
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				if err := a.setSubscriptionFetchInterface("nwg1", filename); err != nil {
					t.Error(err)
				}
				_ = a.Config()
				if err := a.saveConfig(filename); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if err := (&App{}).loadConfig(filename); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionFetchFailurePreservesRules(t *testing.T) {
	for _, fail := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fail {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("keep.example.com\n"))
		}))
		a := &App{config: models.AppConfig{SubscriptionFetchInterface: "mt-no-such-if"}, subscriptionFetch: fetchSubscriptionFixture}
		sub := &models.Subscription{ID: intID.ID{1}, Enable: true, URL: server.URL, Interval: 3600, LastUpdate: 1, Rules: []*models.SubscriptionRule{{ID: intID.ID{2}, Type: models.RuleTypeDomain, Rule: "keep.example.com", Enable: true}}}
		if err := a.AddSubscription(sub); err != nil {
			t.Fatal(err)
		}
		oldRules := sub.Rules
		_, _, err := a.SyncSubscriptionByID(sub.ID, time.Now(), "")
		if (err != nil) != fail {
			t.Fatalf("manual fetch: %v", err)
		}
		sub.LastCheck = 0 // Make the same subscription due for the scheduled path.
		if _, err := a.SyncDueSubscriptions(time.Now()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(oldRules, sub.Rules) || sub.LastUpdate != 1 {
			t.Fatal("download lost previous rules")
		}
		if fail && sub.LastCheck != 0 {
			t.Fatal("failed download marked as checked")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := a.FetchSubscriptionList(ctx, server.URL); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		server.Close()
	}
}
