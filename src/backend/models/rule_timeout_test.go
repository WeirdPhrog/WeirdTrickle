package models

import (
	"strings"
	"testing"
	"time"
)

func TestRegexBacktrackingHasDeadline(t *testing.T) {
	rule := &Rule{Type: RuleTypeRegEx, Rule: `^(a+)+$`}
	started := time.Now()
	if rule.IsMatch(strings.Repeat("a", 200) + "!") {
		t.Fatal("unexpected match")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("regex took %s", elapsed)
	}
	if !rule.IsMatch("aaaa") {
		t.Fatal("timeout poisoned subsequent matches")
	}
}

func TestRegexLookaheadRemainsSupported(t *testing.T) {
	rule := &Rule{Type: RuleTypeRegEx, Rule: `^(?=.*example).*\.com$`}
	if !rule.IsMatch("www.example.com") || rule.IsMatch("www.other.com") {
		t.Fatal("lookahead matching changed")
	}
}

func TestRegexTimeoutCannotExcludePendingRoutingRule(t *testing.T) {
	rule := &Rule{Type: RuleTypeRegEx, Rule: `^(a+)+$`}
	if !rule.MayMatch(strings.Repeat("a", 200) + "!") {
		t.Fatal("a regex timeout was treated as a definitive non-match")
	}
	if rule.MayMatch("unrelated.example") || !rule.MayMatch("aaaa") {
		t.Fatal("completed regex results changed")
	}
	invalid := &Rule{Type: RuleTypeRegEx, Rule: "["}
	if invalid.MayMatch("example") {
		t.Fatal("a pattern that can never compile should not participate")
	}
}

func TestRuleMatchReportsTimeoutWithoutPoisoningSubsequentMatches(t *testing.T) {
	rule := &Rule{Type: RuleTypeRegEx, Rule: `^([a.]+)+$`}
	// A valid DNS name whose comparison cannot finish within the regex limit.
	name := strings.Repeat("a.", 50) + "example"
	if matched, err := rule.Match(name); matched || err == nil {
		t.Fatalf("timeout was treated as a definite non-match: %t, %v", matched, err)
	}
	if matched, err := rule.Match("a.a"); !matched || err != nil {
		t.Fatalf("later successful match changed: %t, %v", matched, err)
	}
}
