package magitrickle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"magitrickle/app"
	"magitrickle/models"
	"magitrickle/subscriptions"
	"magitrickle/utils/intID"

	"github.com/rs/zerolog/log"
)

const subscriptionAutoUpdateTick = time.Minute

type subscriptionRetry struct {
	url   string
	delay time.Duration
	next  time.Time
}

// Caller holds subscriptionSyncMu. Retry fetch/apply failures without changing LastUpdate
// or the user's normal refresh interval; manual sync bypasses this backoff.
func (a *App) subscriptionFailed(id intID.ID, url string, now time.Time) {
	previous := a.subscriptionRetries[id]
	delay := time.Minute
	if previous.url == url {
		delay = min(max(time.Minute, previous.delay*2), 15*time.Minute)
	}
	if a.subscriptionRetries == nil {
		a.subscriptionRetries = make(map[intID.ID]subscriptionRetry)
	}
	a.subscriptionRetries[id] = subscriptionRetry{url: url, delay: delay, next: now.Add(delay)}
}

func (a *App) FetchSubscriptionList(ctx context.Context, url string) (string, error) {
	if a.subscriptionFetch != nil {
		return a.subscriptionFetch(ctx, url, a.Config().SubscriptionFetchInterface)
	}
	return subscriptions.FetchListWithInterface(ctx, url, a.Config().SubscriptionFetchInterface)
}

func (a *App) SetSubscriptionFetchInterface(name string) error {
	return a.setSubscriptionFetchInterface(name, cfgFileLocation)
}

func (a *App) setSubscriptionFetchInterface(name, filename string) error {
	if err := subscriptions.ValidateFetchInterface(name); err != nil {
		return err
	}
	a.configFileMu.Lock()
	defer a.configFileMu.Unlock()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	previous := a.config.SubscriptionFetchInterface
	a.config.SubscriptionFetchInterface = name
	if err := a.saveConfigLocked(filename); err != nil {
		a.config.SubscriptionFetchInterface = previous
		return err
	}
	a.publishConfigLocked()
	return nil
}

func (a *App) SyncSubscriptionRuleSets() error {
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	return a.syncSubscriptionRuleSetsLocked()
}

func (a *App) syncSubscriptionRuleSetsLocked() error {
	for _, sub := range a.subscriptions {
		if sub == nil {
			return fmt.Errorf("null subscription")
		}
		if err := subscriptions.ValidateRules(sub.Rules); err != nil {
			return err
		}
	}
	specs := subscriptions.BuildRuntimeRuleSets(a.subscriptions)
	a.publishDNSRulesLocked(specs)
	defer a.publishDNSRulesLocked(nil)
	next, err := a.reconcileRuleSets(a.subscriptionRuleSets, specs, true)
	if err == nil {
		a.subscriptionRuleSets = next
	}
	return err
}

func disableRuleSets(ruleSets []*RuleSet) error {
	var errs []error
	for _, ruleSet := range ruleSets {
		if err := ruleSet.Disable(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) StartSubscriptionAutoUpdate(ctx context.Context) {
	if a == nil {
		return
	}

	if _, err := a.syncDueSubscriptions(ctx, time.Now()); err != nil {
		log.Error().Err(err).Msg("failed to sync subscriptions")
	}

	ticker := time.NewTicker(subscriptionAutoUpdateTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.syncDueSubscriptions(ctx, time.Now()); err != nil {
				log.Error().Err(err).Msg("failed to sync subscriptions")
			}
		}
	}
}

func (a *App) SyncSubscriptionByID(id intID.ID, now time.Time, urlOverride string) (app.SubscriptionSyncResult, bool, error) {
	return a.SyncSubscriptionByIDContext(context.Background(), id, now, urlOverride)
}

func (a *App) SyncSubscriptionByIDContext(ctx context.Context, id intID.ID, now time.Time, urlOverride string) (app.SubscriptionSyncResult, bool, error) {
	started := time.Now()
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	a.stateMu.RLock()
	sub := findSubscriptionByID(a.subscriptions, id)
	if sub == nil {
		a.stateMu.RUnlock()
		return app.SubscriptionSyncResult{}, false, app.ErrSubscriptionNotFound
	}
	fetchURL := urlOverride
	if fetchURL == "" {
		fetchURL = sub.URL
	}
	existingRules := sub.Rules
	a.stateMu.RUnlock()

	if fetchURL == "" {
		return app.SubscriptionSyncResult{}, false, app.ErrSubscriptionInvalid
	}

	list, err := a.FetchSubscriptionList(ctx, fetchURL)
	if err != nil {
		if ctx.Err() == nil {
			a.subscriptionFailed(id, fetchURL, now.Add(time.Since(started)))
		}
		return app.SubscriptionSyncResult{}, false, fmt.Errorf("%w: %v", app.ErrSubscriptionFetch, err)
	}
	refreshed, rulesChanged, err := subscriptions.PlanRefresh(existingRules, list)
	if err != nil {
		a.subscriptionFailed(id, fetchURL, now.Add(time.Since(started)))
		return app.SubscriptionSyncResult{}, false, fmt.Errorf("%w: %v", app.ErrSubscriptionFetch, err)
	}
	nowSeconds := uint32(now.Unix())

	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	current := findSubscriptionByID(a.subscriptions, id)
	if current == nil {
		return app.SubscriptionSyncResult{}, false, app.ErrSubscriptionNotFound
	}

	urlChanged := current.URL != fetchURL
	prevURL := current.URL
	prevLastCheck := current.LastCheck
	prevLastUpdate := current.LastUpdate
	prevRules := current.Rules

	current.URL = fetchURL
	current.LastCheck = nowSeconds

	if rulesChanged {
		current.Rules = refreshed
		current.LastUpdate = nowSeconds
		if err := a.syncSubscriptionRuleSetsLocked(); err != nil {
			a.subscriptionFailed(id, fetchURL, now.Add(time.Since(started)))
			current.URL = prevURL
			current.LastCheck = prevLastCheck
			current.LastUpdate = prevLastUpdate
			current.Rules = prevRules
			if rollbackErr := a.syncSubscriptionRuleSetsLocked(); rollbackErr != nil {
				return app.SubscriptionSyncResult{}, true, errors.Join(err, fmt.Errorf("failed to rollback subscription sync: %w", rollbackErr))
			}
			return app.SubscriptionSyncResult{}, true, err
		}
	}
	delete(a.subscriptionRetries, id)

	return app.SubscriptionSyncResult{
		URL:        current.URL,
		LastUpdate: current.LastUpdate,
		Rules:      current.Rules,
	}, urlChanged || rulesChanged, nil
}

func (a *App) SyncDueSubscriptions(now time.Time) (bool, error) {
	return a.syncDueSubscriptions(context.Background(), now)
}

func (a *App) syncDueSubscriptions(ctx context.Context, now time.Time) (bool, error) {
	started := time.Now()
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	type dueRef struct {
		id            intID.ID
		url           string
		existingRules []*models.SubscriptionRule
	}

	a.stateMu.RLock()
	var due []dueRef
	for _, sub := range a.subscriptions {
		if !subscriptions.IsDue(sub, now) {
			continue
		}
		if retry := a.subscriptionRetries[sub.ID]; retry.url == sub.URL && now.Before(retry.next) {
			continue
		}
		due = append(due, dueRef{
			id:            sub.ID,
			url:           sub.URL,
			existingRules: sub.Rules,
		})
	}
	a.stateMu.RUnlock()

	if len(due) == 0 {
		return false, nil
	}

	type planResult struct {
		id        intID.ID
		url       string
		refreshed []*models.SubscriptionRule
		changed   bool
	}
	plans := make([]planResult, 0, len(due))
	for _, item := range due {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		list, err := a.FetchSubscriptionList(ctx, item.url)
		if err != nil {
			if ctx.Err() == nil {
				a.subscriptionFailed(item.id, item.url, now.Add(time.Since(started)))
			}
			log.Error().Err(err).Str("subscription", item.id.String()).Msg("failed to fetch subscription")
			continue
		}
		refreshed, changed, err := subscriptions.PlanRefresh(item.existingRules, list)
		if err != nil {
			a.subscriptionFailed(item.id, item.url, now.Add(time.Since(started)))
			log.Error().Err(err).Str("subscription", item.id.String()).Msg("invalid subscription rules; retaining the previous list")
			continue
		}
		plans = append(plans, planResult{
			id:        item.id,
			url:       item.url,
			refreshed: refreshed,
			changed:   changed,
		})
	}

	if len(plans) == 0 {
		return false, nil
	}

	nowSeconds := uint32(now.Unix())

	type rollbackEntry struct {
		sub        *models.Subscription
		rules      []*models.SubscriptionRule
		lastCheck  uint32
		lastUpdate uint32
	}

	a.stateMu.Lock()

	var rollback []rollbackEntry
	anyChanged := false

	for _, p := range plans {
		sub := findSubscriptionByID(a.subscriptions, p.id)
		if sub == nil {
			continue
		}
		rollback = append(rollback, rollbackEntry{
			sub:        sub,
			rules:      sub.Rules,
			lastCheck:  sub.LastCheck,
			lastUpdate: sub.LastUpdate,
		})
		sub.LastCheck = nowSeconds
		if p.changed {
			sub.Rules = p.refreshed
			sub.LastUpdate = nowSeconds
			anyChanged = true
		}
	}

	if !anyChanged {
		for _, p := range plans {
			delete(a.subscriptionRetries, p.id)
		}
		a.stateMu.Unlock()
		return false, nil
	}

	if err := a.syncSubscriptionRuleSetsLocked(); err != nil {
		for _, p := range plans {
			a.subscriptionFailed(p.id, p.url, now.Add(time.Since(started)))
		}
		for _, r := range rollback {
			r.sub.Rules = r.rules
			r.sub.LastCheck = r.lastCheck
			r.sub.LastUpdate = r.lastUpdate
		}
		rollbackErr := a.syncSubscriptionRuleSetsLocked()
		a.stateMu.Unlock()
		if rollbackErr != nil {
			return true, errors.Join(err, fmt.Errorf("failed to rollback subscription sync: %w", rollbackErr))
		}
		return true, err
	}
	for _, p := range plans {
		delete(a.subscriptionRetries, p.id)
	}
	a.stateMu.Unlock()

	if err := a.SaveConfig(); err != nil {
		log.Error().Err(err).Msg("failed to save config file")
	}
	return true, nil
}

func findSubscriptionByID(subs []*models.Subscription, id intID.ID) *models.Subscription {
	for _, sub := range subs {
		if sub != nil && sub.ID == id {
			return sub
		}
	}
	return nil
}
