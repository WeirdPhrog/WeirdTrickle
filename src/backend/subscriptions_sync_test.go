package magitrickle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"magitrickle/app"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func TestEmptyDownloadedSubscriptionRetainsRulesAndBacksOff(t *testing.T) {
	for _, body := range []string{"", " \r\n\t", "# temporarily empty, try later\n"} {
		t.Run(body, func(t *testing.T) {
			var requests atomic.Int32
			var recovered atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if recovered.Load() {
					_, _ = w.Write([]byte("old.example\n"))
				} else {
					_, _ = w.Write([]byte(body))
				}
			}))
			defer server.Close()
			id := intID.ID{1}
			rule := &models.SubscriptionRule{ID: intID.ID{2}, Rule: "old.example", Type: models.RuleTypeDomain, Enable: false}
			sub := &models.Subscription{ID: id, URL: server.URL, Enable: true, Interval: 60, LastCheck: 1, LastUpdate: 1, Rules: []*models.SubscriptionRule{rule}}
			a := &App{subscriptions: []*models.Subscription{sub}}
			now := time.Unix(1800000000, 0)
			for _, offset := range []time.Duration{0, 75 * time.Second, 2 * time.Minute} {
				if changed, err := a.SyncDueSubscriptions(now.Add(offset)); changed || err != nil {
					t.Fatalf("empty auto-refresh changed routing: %t, %v", changed, err)
				}
			}
			if _, changed, err := a.SyncSubscriptionByID(id, now.Add(2*time.Minute), ""); !errors.Is(err, app.ErrSubscriptionFetch) || changed {
				t.Fatalf("empty manual refresh succeeded: %t, %v", changed, err)
			}
			if requests.Load() != 3 || a.subscriptionRetries[id].delay != 4*time.Minute || len(sub.Rules) != 1 || sub.Rules[0] != rule || sub.LastCheck != 1 || sub.LastUpdate != 1 {
				t.Fatal("empty response lost rules, timestamps, or retry backoff")
			}
			recovered.Store(true)
			if _, _, err := a.SyncSubscriptionByID(id, now.Add(2*time.Minute), ""); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 4 || len(a.subscriptionRetries) != 0 || sub.Rules[0] != rule {
				t.Fatal("manual recovery did not retain overrides and clear backoff")
			}
		})
	}
}

func TestSubscriptionFailureBackoffPreservesRulesAndAllowsManualSync(t *testing.T) {
	var requests atomic.Int32
	var success atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !success.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("vpn.example\n"))
	}))
	defer server.Close()
	id := intID.ID{1}
	rule := &models.SubscriptionRule{ID: intID.ID{2}, Rule: "vpn.example", Type: models.RuleTypeDomain, Enable: true}
	sub := &models.Subscription{ID: id, URL: server.URL, Enable: true, Interval: 86400, LastUpdate: 1, Rules: []*models.SubscriptionRule{rule}}
	a := &App{subscriptions: []*models.Subscription{sub}}
	now := time.Unix(1800000000, 0)
	for _, offset := range []time.Duration{0, 75 * time.Second, 2 * time.Minute} {
		if _, err := a.syncDueSubscriptions(context.Background(), now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("retry was not delayed after repeated failures: %d attempts", requests.Load())
	}
	if sub.Rules[0] != rule || sub.LastUpdate != 1 || sub.LastCheck != 0 {
		t.Fatal("failed download changed the last good subscription")
	}
	success.Store(true)
	if _, _, err := a.SyncSubscriptionByID(id, now.Add(2*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || len(a.subscriptionRetries) != 0 {
		t.Fatal("manual sync did not bypass and reset the retry delay")
	}
}

func TestSubscriptionBackoffIsBoundedAndDoesNotDelayChangedURL(t *testing.T) {
	a := &App{}
	id := intID.ID{1}
	now := time.Unix(1800000000, 0)
	for i := 0; i < 20; i++ {
		a.subscriptionFailed(id, "http://old.example", now)
	}
	if retry := a.subscriptionRetries[id]; retry.delay != 15*time.Minute || !retry.next.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("unbounded backoff: %+v", retry)
	}
	a.subscriptionFailed(id, "http://new.example", now)
	if a.subscriptionRetries[id].delay != time.Minute {
		t.Fatal("a different URL inherited the old failure delay")
	}
}

func TestSubscriptionApplyFailureBackoffAndManualRecovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("new.example\n"))
	}))
	defer server.Close()
	id := intID.ID{1}
	oldRules := []*models.SubscriptionRule{{ID: intID.ID{2}, Rule: "old.example", Type: models.RuleTypeDomain, Enable: true}}
	// Duplicate runtime IDs cause a deterministic apply failure without kernel
	// privileges. Successful HTTP downloads must not reset the apply backoff.
	a := &App{subscriptions: []*models.Subscription{
		{ID: id, URL: server.URL, Enable: true, Interval: 60, Rules: oldRules},
		{ID: id, URL: server.URL, Enable: true, Interval: 0, Rules: oldRules},
	}}
	now := time.Unix(1800000000, 0)
	for _, offset := range []time.Duration{0, 75 * time.Second} {
		if _, err := a.SyncDueSubscriptions(now.Add(offset)); err == nil {
			t.Fatal("expected subscription apply failure")
		}
	}
	if _, err := a.SyncDueSubscriptions(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || a.subscriptionRetries[id].delay != 2*time.Minute {
		t.Fatalf("apply failures kept fetching every minute: %d requests, retry=%+v", requests.Load(), a.subscriptionRetries[id])
	}
	if a.subscriptions[0].Rules[0] != oldRules[0] || a.subscriptions[0].LastCheck != 0 {
		t.Fatal("failed apply changed the last good subscription")
	}
	if _, _, err := a.SyncSubscriptionByID(id, now.Add(2*time.Minute), ""); err == nil {
		t.Fatal("manual apply unexpectedly succeeded")
	}
	if requests.Load() != 3 || a.subscriptionRetries[id].delay != 4*time.Minute {
		t.Fatal("a failed manual apply reset the retry delay")
	}
	a.subscriptions = a.subscriptions[:1]
	if _, _, err := a.SyncSubscriptionByID(id, now.Add(2*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 || len(a.subscriptionRetries) != 0 || a.subscriptions[0].Rules[0].Rule != "new.example" {
		t.Fatal("manual recovery did not bypass and reset the apply backoff")
	}
}

func TestUnsupportedDownloadedRuleRetainsRulesAndBacksOff(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("not a supported rule"))
	}))
	defer server.Close()
	id := intID.ID{1}
	rules := []*models.SubscriptionRule{{ID: intID.ID{2}, Rule: "old.example", Type: models.RuleTypeDomain, Enable: true}}
	a := &App{subscriptions: []*models.Subscription{{ID: id, URL: server.URL, Enable: true, Interval: 60, Rules: rules}}}
	now := time.Unix(1800000000, 0)
	for _, offset := range []time.Duration{0, 75 * time.Second, 2 * time.Minute} {
		if _, err := a.SyncDueSubscriptions(now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 || a.subscriptions[0].Rules[0] != rules[0] || a.subscriptions[0].LastUpdate != 0 {
		t.Fatal("an unsupported downloaded rule was published or retried without backoff")
	}
}

func TestSyncSubscriptionByIDUsesOverrideURLAndPersistsURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			_, _ = w.Write([]byte("old.example.com\n"))
		case "/new":
			_, _ = w.Write([]byte("new.example.com\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	subID := intID.ID{0xa1, 0xb2, 0xc3, 0xd4}
	app := &App{
		subscriptions: []*models.Subscription{
			{
				ID:     subID,
				Enable: true,
				URL:    server.URL + "/old",
				Rules: []*models.SubscriptionRule{
					{
						ID:     intID.ID{0x11, 0x22, 0x33, 0x44},
						Rule:   "old.example.com",
						Type:   models.RuleTypeDomain,
						Enable: true,
					},
				},
			},
		},
	}

	updated, changed, err := app.SyncSubscriptionByID(subID, time.Unix(1800000000, 0), server.URL+"/new")
	if err != nil {
		t.Fatalf("SyncSubscriptionByID returned error: %v", err)
	}
	if !changed {
		t.Fatal("SyncSubscriptionByID changed = false, want true")
	}
	if updated.URL != server.URL+"/new" {
		t.Fatalf("updated URL = %q, want %q", updated.URL, server.URL+"/new")
	}
	if len(updated.Rules) != 1 || updated.Rules[0].Rule != "new.example.com" {
		t.Fatalf("updated rules = %#v, want new.example.com", updated.Rules)
	}

	app.WithSubscriptions(func(snapshot []*models.Subscription) {
		if len(snapshot) != 1 {
			t.Fatalf("stored subscriptions = %d, want 1", len(snapshot))
		}
		if snapshot[0].URL != server.URL+"/new" {
			t.Fatalf("stored URL = %q, want %q", snapshot[0].URL, server.URL+"/new")
		}
		if len(snapshot[0].Rules) != 1 || snapshot[0].Rules[0].Rule != "new.example.com" {
			t.Fatalf("stored rules = %#v, want new.example.com", snapshot[0].Rules)
		}
	})
}
