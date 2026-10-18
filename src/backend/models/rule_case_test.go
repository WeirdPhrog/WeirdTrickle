package models

import "testing"

func TestDNSMatchingIgnoresCase(t *testing.T) {
	for _, test := range []struct{ kind, pattern, name string }{
		{RuleTypeDomain, "EXAMPLE.com", "example.COM"},
		{RuleTypeNamespace, "EXAMPLE.com", "SUB.example.COM"},
		{RuleTypeWildcard, "*.EXAMPLE.com", "sub.example.COM"},
	} {
		rule := &Rule{Type: test.kind, Rule: test.pattern}
		if !rule.IsMatch(test.name) {
			t.Errorf("%s %q did not match %q", test.kind, test.pattern, test.name)
		}
	}
}
