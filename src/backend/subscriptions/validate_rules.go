package subscriptions

import (
	"fmt"

	"magitrickle/models"
)

// Reject the entire list instead of silently dropping an enabled unsupported
// rule and allowing its destinations to fall through to another route.
func ValidateRules(rules []*models.SubscriptionRule) error {
	for i, rule := range rules {
		if rule == nil {
			return fmt.Errorf("null subscription rule")
		}
		if rule.Enable {
			if err := models.ValidateRuleType(rule.Type); err != nil {
				return fmt.Errorf("subscription rule %d: %w", i+1, err)
			}
		}
	}
	return nil
}
