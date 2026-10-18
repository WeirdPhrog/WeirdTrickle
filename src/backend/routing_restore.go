package magitrickle

import (
	"fmt"
	"time"

	"magitrickle/models"
)

// Immutable snapshots allow status reads without taking the rule set lock
// while netlink is rebuilding a large set.
type routingRestoreState struct {
	err   string
	delay time.Duration
	next  time.Time
}

// Caller holds g.locker. Successful edits/recovery/disable clear the snapshot.
func (g *RuleSet) recordRestoreFailure(err error, now time.Time) {
	delay := time.Minute
	if previous := g.restoreState.Load(); previous != nil {
		delay = min(max(time.Minute, previous.delay*2), 15*time.Minute)
	}
	g.restoreState.Store(&routingRestoreState{err: fmt.Sprintf("%.512s", err.Error()), delay: delay, next: now.Add(delay)})
	if g.app != nil {
		g.app.routingRestoresPending.Store(true)
	}
}

func (a *App) RoutingStatus() []models.RoutingRestoreStatus {
	statuses := make([]models.RoutingRestoreStatus, 0)
	if !a.routingRestoresPending.Load() {
		return statuses
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	for _, sets := range [][]*RuleSet{a.userRuleSets, a.subscriptionRuleSets} {
		for _, g := range sets {
			if state := g.restoreState.Load(); state != nil {
				statuses = append(statuses, models.RoutingRestoreStatus{
					ID: g.IDValue(), Name: g.DisplayName(), Subscription: g.spec.Subscription,
					Error: state.err, NextRetryAt: state.next.Unix(), RetryDelaySeconds: uint32(state.delay / time.Second),
				})
			}
		}
	}
	return statuses
}
