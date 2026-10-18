package magitrickle

import (
	"errors"
	"fmt"
	"time"

	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/intID"
)

// Only failed rollbacks are retried. Healthy sets and routing choices are not
// rebuilt, and a suspended route is released only after both families restore.
func (a *App) retryRoutingRestores(now time.Time) error {
	if !a.routingRestoresPending.Load() {
		return nil
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	var errs []error
	pending := false
	for _, sets := range [][]*RuleSet{a.userRuleSets, a.subscriptionRuleSets} {
		for _, g := range sets {
			if err := g.retryRestore(now); err != nil {
				errs = append(errs, fmt.Errorf("restore routing for %s: %w", g.RuntimeKey(), err))
			}
			pending = pending || g.restoreState.Load() != nil
		}
	}
	a.routingRestoresPending.Store(pending)
	return errors.Join(errs...)
}

// Caller holds stateMu. Reuse live sets by ID, prepare additions first and
// remove obsolete sets last. Unrelated subscriptions never lose their routes.
func (a *App) reconcileRuleSets(previous []*RuleSet, specs []rulesets.Spec, subscription bool) ([]*RuleSet, error) {
	existing := make(map[intID.ID]*RuleSet, len(previous))
	oldModels := make(map[*RuleSet]*models.Group, len(previous))
	for _, g := range previous {
		existing[g.IDValue()] = g
		oldModels[g] = g.Model()
	}
	seen := make(map[intID.ID]bool, len(specs))
	next := make([]*RuleSet, 0, len(specs))
	for _, spec := range specs {
		if seen[spec.ID] {
			return nil, fmt.Errorf("duplicate rule set %s", spec.ID)
		}
		seen[spec.ID] = true
		g := existing[spec.ID]
		if g == nil {
			var err error
			g, err = newRuleSet(spec, a)
			if err != nil {
				return nil, err
			}
		}
		next = append(next, g)
	}
	order := func(sets []*RuleSet) error {
		if !a.enabled.Load() {
			return nil
		}
		var keys []string
		for _, g := range sets {
			if g.ConfiguredEnabled() {
				keys = append(keys, g.RuntimeKey())
			}
		}
		return a.nfHelper.SetRoutingOrder(keys, subscription)
	}
	rollback := func(cause error) ([]*RuleSet, error) {
		errs := []error{cause}
		for _, g := range next {
			if oldModels[g] == nil {
				errs = append(errs, g.Disable())
			}
		}
		for _, g := range previous {
			errs = append(errs, g.restoreModel(oldModels[g]))
			if a.enabled.Load() {
				errs = append(errs, g.Enable())
			}
		}
		errs = append(errs, order(previous))
		return nil, errors.Join(errs...)
	}
	// New targets must exist before a previous set can stop matching. When
	// several existing lists change, retain their old addresses until all new
	// memberships are ready. Ordinary single-subscription refreshes need one swap.
	changed := 0
	for i, g := range next {
		if old := oldModels[g]; old != nil && !sameRules(old.Rules, modelFromSpec(specs[i]).Rules) {
			changed++
		}
		old, desired := oldModels[g], modelFromSpec(specs[i])
		if a.enabled.Load() && (old == nil || (!old.Enable && desired.Enable)) {
			if err := g.updateModel(desired); err != nil {
				return rollback(err)
			}
			if err := g.Enable(); err != nil {
				return rollback(err)
			}
		}
	}
	for i, g := range next {
		if oldModels[g] != nil {
			if err := g.prepareUpdate(modelFromSpec(specs[i]), changed > 1); err != nil {
				return rollback(err)
			}
		}
	}
	for i, g := range next {
		if err := g.updateModel(modelFromSpec(specs[i])); err != nil {
			return rollback(err)
		}
		if a.enabled.Load() {
			if err := g.Enable(); err != nil {
				return rollback(err)
			}
		}
	}
	for _, g := range previous {
		if !seen[g.IDValue()] {
			if err := g.Disable(); err != nil {
				return rollback(err)
			}
		}
	}
	if err := order(next); err != nil {
		return rollback(err)
	}
	return next, nil
}
