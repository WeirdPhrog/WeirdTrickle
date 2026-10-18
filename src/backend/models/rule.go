package models

import (
	"strings"
	"sync"
	"time"

	"magitrickle/utils/intID"

	"github.com/IGLOU-EU/go-wildcard/v2"
	"github.com/dlclark/regexp2"
)

const (
	RuleTypeDomain    string = "domain"
	RuleTypeNamespace string = "namespace"
	RuleTypeWildcard  string = "wildcard"
	RuleTypeRegEx     string = "regex"
	RuleTypeSubnet    string = "subnet"
	RuleTypeSubnet6   string = "subnet6"
)

type Rule struct {
	ID     intID.ID `yaml:"id"`
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`
	Rule   string   `yaml:"rule"`
	Enable bool     `yaml:"enable"`

	compileOnce sync.Once
	compileWait sync.WaitGroup
	compileErr  error
	compiled    func(string) (bool, error)
}

func (d *Rule) IsEnabled() bool {
	return d.Enable
}

func (d *Rule) Compile() error {
	d.compileOnce.Do(func() {
		d.compileWait.Add(1)
		defer d.compileWait.Done()

		switch d.Type {
		case RuleTypeRegEx:
			re, err := regexp2.Compile(d.Rule, regexp2.IgnoreCase)
			if err != nil {
				d.compileErr = err
				return
			}
			// User/subscription regexes may backtrack exponentially on DNS names.
			re.MatchTimeout = 100 * time.Millisecond
			d.compiled = re.MatchString
		}
	})
	d.compileWait.Wait()
	return d.compileErr
}

func (d *Rule) IsMatch(domainName string) bool {
	matched, _ := d.Match(domainName)
	return matched
}

// Match reports runtime regex errors so routing can fail closed on an
// inconclusive comparison. Invalid/unknown rules remain definite non-matches.
func (d *Rule) Match(domainName string) (bool, error) {
	switch d.Type {
	case RuleTypeDomain:
		return strings.EqualFold(domainName, d.Rule), nil

	case RuleTypeNamespace:
		if strings.EqualFold(domainName, d.Rule) {
			return true, nil
		}

		ruleLen := len(d.Rule)
		domainLen := len(domainName)
		if domainLen < ruleLen+1 {
			return false, nil
		}
		return domainName[domainLen-ruleLen-1] == '.' && strings.EqualFold(domainName[domainLen-ruleLen:], d.Rule), nil

	case RuleTypeWildcard:
		return wildcard.Match(strings.ToLower(d.Rule), strings.ToLower(domainName)), nil

	case RuleTypeRegEx:
		err := d.Compile()
		if err != nil {
			return false, nil
		}
		return d.compiled(domainName)

	case RuleTypeSubnet, RuleTypeSubnet6:
		return false, nil
	}
	return false, nil
}

// MayMatch is a conservative prefilter, not a routing decision. A regex
// timeout is inconclusive and must not let a DNS answer bypass an update lock.
func (d *Rule) MayMatch(domainName string) bool {
	ok, err := d.Match(domainName)
	return ok || err != nil
}
