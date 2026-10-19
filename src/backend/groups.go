package magitrickle

import (
	"fmt"

	"magitrickle/app"
	groupruntime "magitrickle/groups"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func validateGroups(groups []*models.Group) error {
	ids := make(map[intID.ID]bool, len(groups))
	for _, group := range groups {
		if group == nil {
			return fmt.Errorf("null group")
		}
		if ids[group.ID] {
			return ErrGroupIDConflict
		}
		ids[group.ID] = true
		ruleIDs := make(map[intID.ID]bool, len(group.Rules))
		for _, rule := range group.Rules {
			if rule == nil {
				return fmt.Errorf("null rule in group %s", group.ID)
			}
			if rule.Enable {
				if err := models.ValidateRuleType(rule.Type); err != nil {
					return fmt.Errorf("group %s: %w", group.ID, err)
				}
			}
			if ruleIDs[rule.ID] {
				return ErrRuleIDConflict
			}
			ruleIDs[rule.ID] = true
		}
	}
	return nil
}

// API rule operations derive their edits from an immutable request snapshot.
// Reject a stale snapshot rather than silently discarding another request's edit.
func (a *App) UpdateGroupIfCurrent(model, expected *models.Group) error {
	return a.updateGroup(model, expected)
}

func (a *App) updateGroup(model, expected *models.Group) error {
	if err := validateGroups([]*models.Group{model}); err != nil {
		return err
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	for idx, group := range a.userRuleSets {
		if group.IDValue() == model.ID {
			if expected != nil && group.Model() != expected {
				return app.ErrGroupChanged
			}
			models := make([]*models.Group, len(a.userRuleSets))
			for i, current := range a.userRuleSets {
				models[i] = current.Model()
			}
			models[idx] = model
			return a.replaceGroupsLocked(models)
		}
	}
	return app.ErrGroupNotFound
}

// ReplaceGroups validates the whole request before touching live routing and
// restores the previous groups if applying any replacement fails.
func (a *App) ReplaceGroups(models []*models.Group) error {
	if err := validateGroups(models); err != nil {
		return err
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.replaceGroupsLocked(models)
}

func (a *App) replaceGroupsLocked(models []*models.Group) error {
	specs := groupruntime.BuildRuntimeRuleSets(models)
	a.publishDNSRulesLocked(specs)
	defer a.publishDNSRulesLocked(nil)
	next, err := a.reconcileRuleSets(a.userRuleSets, specs, false)
	if err == nil {
		a.userRuleSets = next
	}
	return err
}
