package v1

import (
	"fmt"
	"strings"

	"magitrickle/api/v1/types"
	"magitrickle/models"
	"magitrickle/subscriptions"
	"magitrickle/utils/intID"

	"github.com/dlclark/regexp2"
)

var colorRegExp = regexp2.MustCompile(`^#[0-9a-f]{6}$`, regexp2.IgnoreCase)

func GroupFromReq(req types.GroupReq, existing *models.Group) (*models.Group, error) {
	if err := subscriptions.ValidateFetchInterface(req.Interface); err != nil {
		return nil, fmt.Errorf("invalid routing interface")
	}
	var group *models.Group
	if existing == nil {
		group = &models.Group{ID: intID.RandomID(), Enable: true}
	} else {
		copy := *existing
		group = &copy
	}
	if req.ID != nil {
		if existing != nil && group.ID != *req.ID {
			return nil, fmt.Errorf("group ID mismatch")
		}
		if existing == nil {
			group.ID = *req.ID
		}
	}
	group.Name = req.Name
	group.Color = "#ffffff"
	if match, _ := colorRegExp.MatchString(req.Color); match {
		group.Color = strings.ToLower(req.Color)
	}
	group.Interface = req.Interface
	if req.Enable != nil {
		group.Enable = *req.Enable
	}

	if req.Rules != nil {
		existingIDs := indexRuleIDs(group.Rules)
		newRules := make([]*models.Rule, len(*req.Rules))
		for i, ruleReq := range *req.Rules {
			r, err := ruleFromReq(ruleReq, existingIDs)
			if err != nil {
				return nil, err
			}
			newRules[i] = r
		}
		group.Rules = newRules
	}
	return group, nil
}

func RuleFromReq(ruleReq types.RuleReq, existingRules []*models.Rule) (*models.Rule, error) {
	return ruleFromReq(ruleReq, indexRuleIDs(existingRules))
}

func indexRuleIDs(rules []*models.Rule) map[intID.ID]struct{} {
	ids := make(map[intID.ID]struct{}, len(rules))
	for _, rule := range rules {
		ids[rule.ID] = struct{}{}
	}
	return ids
}

func ruleFromReq(ruleReq types.RuleReq, existingIDs map[intID.ID]struct{}) (*models.Rule, error) {
	var rule *models.Rule
	if ruleReq.ID != nil {
		if _, exists := existingIDs[*ruleReq.ID]; exists {
			rule = &models.Rule{ID: *ruleReq.ID}
		}
	}
	if rule == nil {
		rule = &models.Rule{
			ID: intID.RandomID(),
		}
	}
	rule.Name = ruleReq.Name
	rule.Type = ruleReq.Type
	rule.Rule = ruleReq.Rule
	rule.Enable = ruleReq.Enable
	return rule, nil
}

func RespFromGroups(groups []*models.Group, withRules bool) types.GroupsRes {
	groupResList := make([]types.GroupRes, len(groups))
	for i, group := range groups {
		groupResList[i] = RespFromGroup(group, withRules)
	}
	return types.GroupsRes{Groups: &groupResList}
}

func RespFromGroup(group *models.Group, withRules bool) types.GroupRes {
	groupRes := types.GroupRes{
		ID:        group.ID,
		Name:      group.Name,
		Color:     group.Color,
		Interface: group.Interface,
		Enable:    group.Enable,
	}
	if withRules {
		groupRes.RulesRes = RespFromRules(group.Rules)
	}
	return groupRes
}

func RespFromRules(rules []*models.Rule) types.RulesRes {
	ruleResList := make([]types.RuleRes, len(rules))
	for i, rule := range rules {
		ruleResList[i] = RespFromRule(rule)
	}
	return types.RulesRes{Rules: &ruleResList}
}

func RespFromRule(rule *models.Rule) types.RuleRes {
	return types.RuleRes{
		ID:     rule.ID,
		Name:   rule.Name,
		Type:   rule.Type,
		Rule:   rule.Rule,
		Enable: rule.Enable,
	}
}
