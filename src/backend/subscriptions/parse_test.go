package subscriptions

import (
	"strings"
	"testing"

	"magitrickle/models"
	"magitrickle/utils/intID"
)

func TestParseRulesNormalizesPlainDomainLists(t *testing.T) {
	list := "\ufeffExample.COM. # comment, not-a-rule\r\n.ua\rпример.рф\n192.0.2.0/24,2001:db8::/32\n*.EXAMPLE.org\nexample.com\n# comment\n"
	want := []struct{ pattern, kind string }{
		{"example.com", models.RuleTypeNamespace},
		{"ua", models.RuleTypeNamespace},
		{"xn--e1afmkfd.xn--p1ai", models.RuleTypeNamespace},
		{"192.0.2.0/24", models.RuleTypeSubnet},
		{"2001:db8::/32", models.RuleTypeSubnet6},
		{"*.example.org", models.RuleTypeWildcard},
	}
	rules := ParseRules(list)
	if len(rules) != len(want) {
		t.Fatalf("parsed %d rules, want %d: %+v", len(rules), len(want), rules)
	}
	ids := make(map[intID.ID]bool)
	for i, rule := range rules {
		if rule.Rule != want[i].pattern || rule.Type != want[i].kind || !rule.Enable || rule.ID.IsZero() || ids[rule.ID] {
			t.Fatalf("rule %d: %+v, want %+v with a unique ID", i, rule, want[i])
		}
		ids[rule.ID] = true
	}
	if err := ValidateRules(rules); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizationDoesNotSilentlyDropUnsupportedEntries(t *testing.T) {
	for _, pattern := range []string{".", "..", ".*example.com", "*.example.com.", ".192.0.2.1", "192.0.2.1.", ".192.0.2.0/24", "192.0.2.0/24.", ".2001:db8::1", "2001:db8::1.", "not a domain", "https://example.com", "пример..рф"} {
		rules := ParseRules("valid.example\n" + pattern)
		if len(rules) != 2 || ValidateRules(rules) == nil {
			t.Fatalf("unsupported entry %q was dropped or accepted: %+v", pattern, rules)
		}
	}
}

func TestPlanRefreshRetainsConflictingCanonicalOverrides(t *testing.T) {
	for _, alias := range []*models.SubscriptionRule{
		{ID: intID.ID{2}, Rule: "EXAMPLE.com", Type: models.RuleTypeNamespace, Enable: true},
		{ID: intID.ID{2}, Rule: ".example.com.", Type: models.RuleTypeDomain, Enable: false},
	} {
		existing := []*models.SubscriptionRule{{ID: intID.ID{1}, Rule: "example.com", Type: models.RuleTypeNamespace, Enable: false}, alias}
		if rules, changed, err := PlanRefresh(existing, "example.com"); err == nil || changed || rules != nil {
			t.Fatalf("conflicting aliases silently changed routing: %+v, %t, %v", rules, changed, err)
		}
	}
}

func TestRefreshRulesPreservesOverridesAcrossNormalization(t *testing.T) {
	existing := []*models.SubscriptionRule{
		{ID: intID.ID{1}, Rule: "Example.COM.", Type: models.RuleTypeDomain, Enable: false},
		{ID: intID.ID{2}, Rule: "пример.рф", Type: models.RuleTypeNamespace, Enable: false},
	}
	refreshed, err := RefreshRules("example.com\nxn--e1afmkfd.xn--p1ai", existing)
	if err != nil {
		t.Fatal(err)
	}
	for i, rule := range refreshed {
		if rule.ID != existing[i].ID || rule.Enable || rule.Type != existing[i].Type {
			t.Fatalf("normalization reset rule overrides: %+v", rule)
		}
	}
	if len(refreshed) != 2 || existing[0].Rule != "Example.COM." || existing[1].Rule != "пример.рф" {
		t.Fatal("normalization mutated the old rules or lost a rule")
	}
}

func TestPlanRefreshRetainsPreviousRulesOnEmptyOrUnsupportedResponse(t *testing.T) {
	existing := []*models.SubscriptionRule{{ID: intID.ID{1}, Rule: "old.example", Type: models.RuleTypeNamespace, Enable: true}}
	for _, list := range []string{"", " \r\n\t", "# comment\n# another", "valid.example\nnot a domain"} {
		refreshed, changed, err := PlanRefresh(existing, list)
		if err == nil || changed || refreshed != nil || existing[0].Rule != "old.example" {
			t.Fatalf("invalid refresh %q erased or replaced rules: %+v, %t, %v", list, refreshed, changed, err)
		}
	}
	refreshed, changed, err := PlanRefresh(nil, "")
	if err != nil || changed || len(refreshed) != 0 {
		t.Fatalf("an initially empty subscription failed: %v", err)
	}
	refreshed, changed, err = PlanRefresh(existing, ".UA. # list updated\n")
	if err != nil || !changed || len(refreshed) != 1 || refreshed[0].Rule != "ua" {
		t.Fatalf("a normalized replacement failed: %+v, %v", refreshed, err)
	}
	_, _, err = PlanRefresh(existing, "bad entry")
	if !strings.Contains(err.Error(), "rule 1") || strings.Contains(err.Error(), "bad entry") {
		t.Fatal("invalid rule index missing or server response exposed")
	}
	_, _, err = PlanRefresh(existing, strings.Repeat("not a domain ", 1024))
	if err == nil || len(err.Error()) > 512 || strings.Contains(err.Error(), "not a domain") {
		t.Fatal("invalid server content was exposed in the diagnostic")
	}
}

func TestRefreshRulesPreservesExistingOverrides(t *testing.T) {
	existing := []*models.SubscriptionRule{
		{
			ID:     intID.ID{0xaa, 0xbb, 0xcc, 0xdd},
			Rule:   "example.com",
			Type:   models.RuleTypeDomain,
			Enable: false,
		},
		{
			ID:     intID.ID{0x11, 0x22, 0x33, 0x44},
			Rule:   "*.example.org",
			Type:   models.RuleTypeWildcard,
			Enable: true,
		},
	}

	refreshed, err := RefreshRules("example.com\nsub.example.net\n", existing)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 2 {
		t.Fatalf("expected 2 refreshed rules, got %d", len(refreshed))
	}

	if refreshed[0].Rule != "example.com" {
		t.Fatalf("expected first rule to stay aligned with parsed order, got %q", refreshed[0].Rule)
	}
	if refreshed[0].ID != existing[0].ID {
		t.Fatalf("expected existing ID to be preserved, got %s want %s", refreshed[0].ID.String(), existing[0].ID.String())
	}
	if refreshed[0].Type != existing[0].Type {
		t.Fatalf("expected existing type to be preserved, got %q want %q", refreshed[0].Type, existing[0].Type)
	}
	if refreshed[0].Enable != existing[0].Enable {
		t.Fatalf("expected existing enabled flag to be preserved, got %v want %v", refreshed[0].Enable, existing[0].Enable)
	}

	if refreshed[1].Rule != "sub.example.net" {
		t.Fatalf("expected second parsed rule to be kept, got %q", refreshed[1].Rule)
	}
	if refreshed[1].ID.IsZero() {
		t.Fatal("expected new rule to receive an ID")
	}
	if refreshed[1].Enable != true {
		t.Fatalf("expected new rule to default to enabled, got %v", refreshed[1].Enable)
	}
}

func TestRefreshRulesReplacesUnsupportedSavedTypesWithoutEnablingRules(t *testing.T) {
	existing := []*models.SubscriptionRule{
		{ID: intID.ID{1}, Rule: ".ua", Type: "removed-type", Enable: false},
		{ID: intID.ID{2}, Rule: "example.com", Type: models.RuleTypeDomain, Enable: false},
	}
	rules, changed, err := PlanRefresh(existing, ".ua\nexample.com\n")
	if err != nil || !changed || len(rules) != 2 {
		t.Fatalf("failed to refresh an obsolete saved type: %+v, %t, %v", rules, changed, err)
	}
	if rules[0].Type != models.RuleTypeNamespace || rules[0].Rule != "ua" || rules[1].Type != models.RuleTypeDomain {
		t.Fatalf("obsolete type was retained or supported override lost: %+v", rules)
	}
	for i, rule := range rules {
		if rule.Enable || rule.ID != existing[i].ID {
			t.Fatal("re-detecting a type changed the ID or enabled the rule")
		}
	}
	if existing[0].Type != "removed-type" || existing[0].Rule != ".ua" {
		t.Fatal("refresh mutated the original snapshot")
	}
	// A discarded type is not a conflicting override for a canonical alias.
	existing = append(existing[:1], &models.SubscriptionRule{ID: intID.ID{3}, Rule: "ua", Type: models.RuleTypeNamespace, Enable: false})
	if _, _, err := PlanRefresh(existing, ".ua"); err != nil {
		t.Fatalf("obsolete type falsely conflicted with its supported alias: %v", err)
	}
}

func TestSameRulesIgnoresOrder(t *testing.T) {
	left := []*models.SubscriptionRule{
		{ID: intID.ID{0x01, 0x02, 0x03, 0x04}, Rule: "example.com", Type: models.RuleTypeDomain, Enable: true},
		{ID: intID.ID{0x05, 0x06, 0x07, 0x08}, Rule: "*.example.org", Type: models.RuleTypeWildcard, Enable: false},
	}
	right := []*models.SubscriptionRule{
		{ID: intID.ID{0x05, 0x06, 0x07, 0x08}, Rule: "*.example.org", Type: models.RuleTypeWildcard, Enable: false},
		{ID: intID.ID{0x01, 0x02, 0x03, 0x04}, Rule: "example.com", Type: models.RuleTypeDomain, Enable: true},
	}

	if !sameRules(left, right) {
		t.Fatal("expected rule comparison to ignore order for equal content")
	}
}
