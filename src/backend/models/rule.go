package models

import (
	"fmt"
	"strings"

	"magitrickle/utils/intID"

	"github.com/IGLOU-EU/go-wildcard/v2"
)

const (
	RuleTypeDomain    string = "domain"
	RuleTypeNamespace string = "namespace"
	RuleTypeWildcard  string = "wildcard"
	RuleTypeSubnet    string = "subnet"
	RuleTypeSubnet6   string = "subnet6"
)

type Rule struct {
	ID     intID.ID `yaml:"id"`
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`
	Rule   string   `yaml:"rule"`
	Enable bool     `yaml:"enable"`
}

func (d *Rule) IsEnabled() bool {
	return d.Enable
}

func ValidateRuleType(ruleType string) error {
	switch ruleType {
	case RuleTypeDomain, RuleTypeNamespace, RuleTypeWildcard, RuleTypeSubnet, RuleTypeSubnet6:
		return nil
	default:
		return fmt.Errorf("unsupported rule type %q", ruleType)
	}
}

func (d *Rule) IsMatch(domainName string) bool {
	switch d.Type {
	case RuleTypeDomain:
		return strings.EqualFold(domainName, d.Rule)

	case RuleTypeNamespace:
		if strings.EqualFold(domainName, d.Rule) {
			return true
		}

		ruleLen := len(d.Rule)
		domainLen := len(domainName)
		if domainLen < ruleLen+1 {
			return false
		}
		return domainName[domainLen-ruleLen-1] == '.' && strings.EqualFold(domainName[domainLen-ruleLen:], d.Rule)

	case RuleTypeWildcard:
		return wildcard.Match(strings.ToLower(d.Rule), strings.ToLower(domainName))
	}
	return false
}
