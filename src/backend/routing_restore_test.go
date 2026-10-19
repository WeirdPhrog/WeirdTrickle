package magitrickle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/intID"
)

func TestRoutingRestoreBackoffAndStatusSnapshots(t *testing.T) {
	a := &App{}
	g := newRuleSet(rulesets.Spec{ID: intID.ID{1}, Name: "VPN group", Enable: true}, a)
	sub := newRuleSet(rulesets.Spec{ID: intID.ID{1}, Name: "ASN subscription", Enable: true, Subscription: true}, a)
	a.userRuleSets, a.subscriptionRuleSets = []*RuleSet{g}, []*RuleSet{sub}
	now := time.Unix(1800000000, 0)
	for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		g.recordRestoreFailure(errors.New("not enough memory"), now)
		state := g.restoreState.Load()
		if state.delay != delay || !state.next.Equal(now.Add(delay)) || !a.routingRestoresPending.Load() {
			t.Fatalf("incorrect restore backoff: %+v", state)
		}
	}
	first := a.RoutingStatus()
	if len(first) != 1 || first[0].Subscription || first[0].Error != "not enough memory" || first[0].RetryDelaySeconds != 900 || first[0].NextRetryAt != now.Add(15*time.Minute).Unix() {
		t.Fatalf("incorrect runtime status: %+v", first)
	}
	first[0].Name = "changed by API caller"
	sub.recordRestoreFailure(errors.New(strings.Repeat("x", 1024)), now)
	status := a.RoutingStatus()
	if len(status) != 2 || status[0].Name != "VPN group" || !status[1].Subscription || len(status[1].Error) > 512 {
		t.Fatalf("status mixed groups/subscriptions or exposed a mutable/unbounded snapshot: %+v", status)
	}
	// A stopped set is no longer suspended, even if a stale snapshot survived.
	if err := a.retryRoutingRestores(now.Add(time.Hour)); err != nil || len(a.RoutingStatus()) != 0 || a.routingRestoresPending.Load() {
		t.Fatal("disabled sets retained recovery state")
	}
	g.recordRestoreFailure(errors.New("new failure"), now)
	if g.restoreState.Load().delay != time.Minute {
		t.Fatal("a fresh failure inherited the previous recovery delay")
	}
}

func TestHealthyRoutingMaintenanceDoesNotAcquireStateLock(t *testing.T) {
	a := &App{}
	a.stateMu.Lock()
	done := make(chan error, 1)
	go func() {
		if status := a.RoutingStatus(); len(status) != 0 {
			done <- errors.New("healthy routing was marked suspended")
			return
		}
		done <- a.retryRoutingRestores(time.Now())
	}()
	select {
	case err := <-done:
		a.stateMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		a.stateMu.Unlock()
		<-done
		t.Fatal("healthy routing maintenance acquired the common state lock")
	}
}

func TestPendingRecoveryWaitsWithoutLockingRuleSet(t *testing.T) {
	g := newRuleSet(rulesets.Spec{Model: &models.Group{Enable: true}}, &App{})
	now := time.Now()
	g.recordRestoreFailure(errors.New("kernel busy"), now)
	g.locker.Lock()
	done := make(chan error, 1)
	go func() { done <- g.retryRestore(now) }()
	select {
	case err := <-done:
		g.locker.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		g.locker.Unlock()
		<-done
		t.Fatal("a delayed recovery took the rule set lock")
	}
}
