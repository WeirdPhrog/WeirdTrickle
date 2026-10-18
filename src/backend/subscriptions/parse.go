package subscriptions

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"magitrickle/models"
	"magitrickle/utils/intID"

	"golang.org/x/net/idna"
)

func ParseRules(list string) []*models.SubscriptionRule {
	rules := make([]*models.SubscriptionRule, 0)
	lines := strings.FieldsFunc(list, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	seenRules := make(map[string]struct{}, len(lines))
	usedRuleIDs := make(map[[4]byte]struct{}, len(lines))

	for _, line := range lines {
		// Remove comments before splitting comma-separated entries: commas in
		// a comment must not turn into new routing rules.
		line, _, _ = strings.Cut(line, "#")
		for _, part := range strings.Split(line, ",") {
			pattern := normalizeSubscriptionRule(part)
			if pattern == "" {
				continue
			}

			ruleType := detectSubscriptionRuleType(pattern)
			key := ruleType + "|" + pattern
			if _, exists := seenRules[key]; exists {
				continue
			}
			seenRules[key] = struct{}{}

			rules = append(rules, &models.SubscriptionRule{
				ID:     nextUniqueRuleID(usedRuleIDs),
				Rule:   pattern,
				Type:   ruleType,
				Enable: true,
			})
		}
	}

	return rules
}

func normalizeSubscriptionRule(pattern string) string {
	pattern = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pattern), "\ufeff"))
	original := pattern
	// A leading dot denotes a namespace in plain domain lists. Do not strip
	// it from wildcard/unsupported expressions and change their meaning.
	if !strings.ContainsAny(pattern, "*?") {
		pattern = strings.TrimPrefix(pattern, ".")
		pattern = strings.TrimSuffix(pattern, ".")
	}
	for i := 0; i < len(pattern); i++ {
		if pattern[i] >= utf8.RuneSelf {
			ascii, err := idna.Lookup.ToASCII(pattern)
			if err != nil {
				return original
			}
			pattern = ascii
			break
		}
	}
	if pattern != original && detectSubscriptionRuleType(pattern) == "" {
		return original
	}
	return strings.ToLower(pattern)
}

func RefreshRules(list string, existing []*models.SubscriptionRule) ([]*models.SubscriptionRule, error) {
	parsed := ParseRules(list)
	if len(parsed) == 0 {
		return parsed, nil
	}

	existingByRule := make(map[string]*models.SubscriptionRule, len(existing))
	for _, rule := range existing {
		if rule == nil || rule.Rule == "" {
			continue
		}
		key := normalizeSubscriptionRule(rule.Rule)
		if old := existingByRule[key]; old != nil {
			// Canonical aliases may carry different user overrides. Choosing
			// one silently could disable previously effective routing.
			if old.Rule != rule.Rule && (old.Type != rule.Type || old.Enable != rule.Enable) {
				return nil, fmt.Errorf("conflicting overrides for normalized subscription rule %q; retaining the previous rules", key)
			}
			continue
		}
		existingByRule[key] = rule
	}

	usedRuleIDs := make(map[[4]byte]struct{}, len(parsed))
	for _, rule := range parsed {
		if current := existingByRule[rule.Rule]; current != nil {
			rule.ID = current.ID
			rule.Enable = current.Enable
			if current.Type != "" {
				rule.Type = current.Type
			}
		}
		if rule.ID.IsZero() {
			rule.ID = nextUniqueRuleID(usedRuleIDs)
			continue
		}
		if _, exists := usedRuleIDs[rule.ID]; exists {
			rule.ID = nextUniqueRuleID(usedRuleIDs)
			continue
		}
		usedRuleIDs[rule.ID] = struct{}{}
	}

	return parsed, nil
}

type subscriptionRuleState struct {
	ID     intID.ID
	Enable bool
	Count  int
}

func sameRulesKey(rule *models.SubscriptionRule) string {
	if rule == nil {
		return ""
	}

	ruleType := rule.Type
	if ruleType == "" {
		ruleType = detectSubscriptionRuleType(rule.Rule)
	}

	return ruleType + "|" + rule.Rule
}

func sameRules(left, right []*models.SubscriptionRule) bool {
	if len(left) != len(right) {
		return false
	}
	if len(left) == 0 {
		return true
	}

	states := make(map[string]subscriptionRuleState, len(left))
	leftNilCount := 0
	for _, rule := range left {
		if rule == nil {
			leftNilCount++
			continue
		}

		key := sameRulesKey(rule)
		state := states[key]
		if state.Count > 0 && (state.ID != rule.ID || state.Enable != rule.Enable) {
			return false
		}
		state.ID = rule.ID
		state.Enable = rule.Enable
		state.Count++
		states[key] = state
	}

	rightNilCount := 0
	for _, rule := range right {
		if rule == nil {
			rightNilCount++
			continue
		}

		key := sameRulesKey(rule)
		state, exists := states[key]
		if !exists || state.Count == 0 {
			return false
		}
		if state.ID != rule.ID || state.Enable != rule.Enable {
			return false
		}
		state.Count--
		if state.Count == 0 {
			delete(states, key)
			continue
		}
		states[key] = state
	}

	return leftNilCount == rightNilCount && len(states) == 0
}

func detectSubscriptionRuleType(pattern string) string {
	p := strings.TrimSpace(pattern)

	if isValidSubnet6(p) {
		return "subnet6"
	}
	if isValidSubnet(p) {
		return "subnet"
	}
	if isValidNamespace(p) {
		return "namespace"
	}
	if isValidDomain(p) {
		return "domain"
	}
	if isValidWildcard(p) {
		return "wildcard"
	}

	return ""
}

func nextUniqueRuleID(used map[[4]byte]struct{}) intID.ID {
	for {
		candidate := intID.RandomID()
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
}
