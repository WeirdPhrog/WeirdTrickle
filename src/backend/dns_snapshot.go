package magitrickle

import (
	"magitrickle/models"
	"magitrickle/rulesets"
)

// A conservative, immutable filter lets unrelated DNS answers bypass stateMu
// while a large routing update is in progress. Matching answers still use the
// locked live state, so they cannot escape before their routes are installed.
type dnsRuleSnapshot struct {
	rules []*models.Rule
}

func (a *App) dnsRuleSetSnapshot(names []string) []*RuleSet {
	if a.stateMu.TryRLock() {
		defer a.stateMu.RUnlock()
		return a.ruleSetSnapshotLocked()
	}
	if snapshot := a.dnsRules.Load(); snapshot != nil && !snapshot.matches(names) {
		return nil
	}
	return a.ruleSetSnapshot()
}

func (s *dnsRuleSnapshot) matches(names []string) bool {
	for _, rule := range s.rules {
		for _, name := range names {
			if rule.IsMatch(name) {
				return true
			}
		}
	}
	return false
}

// Caller holds stateMu. Include both the old and proposed rules until the
// transaction (or rollback) finishes, including new groups and subscriptions.
func (a *App) publishDNSRulesLocked(pending []rulesets.Spec) {
	snapshot := &dnsRuleSnapshot{}
	appendGroup := func(group *models.Group) {
		if group == nil || !group.Enable {
			return
		}
		for _, rule := range group.Rules {
			if !rule.Enable {
				continue
			}
			switch rule.Type {
			case models.RuleTypeDomain, models.RuleTypeNamespace, models.RuleTypeWildcard:
				snapshot.rules = append(snapshot.rules, rule)
			}
		}
	}
	for _, group := range a.userRuleSets {
		appendGroup(group.Model())
	}
	for _, group := range a.subscriptionRuleSets {
		appendGroup(group.Model())
	}
	for _, spec := range pending {
		appendGroup(modelFromSpec(spec))
	}
	a.dnsRules.Store(snapshot)
}
