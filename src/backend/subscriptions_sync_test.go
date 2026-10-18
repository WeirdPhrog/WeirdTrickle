package magitrickle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"magitrickle/models"
	"magitrickle/utils/intID"
)

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
		a.subscriptionFetchFailed(id, "http://old.example", now)
	}
	if retry := a.subscriptionRetries[id]; retry.delay != 15*time.Minute || !retry.next.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("unbounded backoff: %+v", retry)
	}
	a.subscriptionFetchFailed(id, "http://new.example", now)
	if a.subscriptionRetries[id].delay != time.Minute {
		t.Fatal("a different URL inherited the old failure delay")
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
