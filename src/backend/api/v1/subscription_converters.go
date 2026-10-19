package v1

import (
	"fmt"

	"magitrickle/api/v1/types"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func SubscriptionFromReq(req types.SubscriptionReq, existing *models.Subscription) (*models.Subscription, error) {
	if !models.IsValidInterfaceName(req.Interface) {
		return nil, fmt.Errorf("invalid routing interface")
	}
	sub := &models.Subscription{Enable: true}
	if existing != nil {
		*sub = *existing
	} else {
		sub.ID = intID.RandomID()
	}
	if req.ID != nil {
		if existing != nil && sub.ID != *req.ID {
			return nil, fmt.Errorf("subscription ID mismatch")
		}
		if existing == nil {
			sub.ID = *req.ID
		}
	}
	sub.Name = req.Name
	sub.Interface = req.Interface
	sub.URL = req.URL
	if req.Enable != nil {
		sub.Enable = *req.Enable
	}
	if req.Interval != nil {
		sub.Interval = *req.Interval
	}
	if req.LastUpdate != nil {
		sub.LastUpdate = *req.LastUpdate
	}

	if req.Rules != nil {
		var existingRules []*models.SubscriptionRule
		if existing != nil {
			existingRules = existing.Rules
		}
		existingIDs := indexSubscriptionRuleIDs(existingRules)
		newRules := make([]*models.SubscriptionRule, len(*req.Rules))
		for i, ruleReq := range *req.Rules {
			r, err := subscriptionRuleFromReq(ruleReq, existingIDs)
			if err != nil {
				return nil, err
			}
			newRules[i] = r
		}
		sub.Rules = newRules
	} else if existing != nil {
		sub.Rules = existing.Rules
	}

	return sub, nil
}

func SubscriptionRuleFromReq(ruleReq types.SubscriptionRuleReq, existingRules []*models.SubscriptionRule) (*models.SubscriptionRule, error) {
	return subscriptionRuleFromReq(ruleReq, indexSubscriptionRuleIDs(existingRules))
}

func indexSubscriptionRuleIDs(rules []*models.SubscriptionRule) map[intID.ID]struct{} {
	ids := make(map[intID.ID]struct{}, len(rules))
	for _, rule := range rules {
		ids[rule.ID] = struct{}{}
	}
	return ids
}

func subscriptionRuleFromReq(ruleReq types.SubscriptionRuleReq, existingIDs map[intID.ID]struct{}) (*models.SubscriptionRule, error) {
	rule := &models.SubscriptionRule{}
	if ruleReq.ID != nil {
		if _, exists := existingIDs[*ruleReq.ID]; exists {
			rule.ID = *ruleReq.ID
		}
	}
	if rule.ID.IsZero() {
		rule.ID = intID.RandomID()
	}
	rule.Rule = ruleReq.Rule
	rule.Type = ruleReq.Type
	rule.Enable = ruleReq.Enable
	if rule.Enable {
		if err := models.ValidateRuleType(rule.Type); err != nil {
			return nil, err
		}
	}
	return rule, nil
}

func RespFromSubscriptions(subs []*models.Subscription) types.SubscriptionsRes {
	list := make([]types.SubscriptionRes, len(subs))
	for i, sub := range subs {
		list[i] = RespFromSubscription(sub, true)
	}
	return types.SubscriptionsRes{Subscriptions: &list}
}

func RespFromSubscription(sub *models.Subscription, withRules bool) types.SubscriptionRes {
	res := types.SubscriptionRes{
		ID:         sub.ID,
		Name:       sub.Name,
		Interface:  sub.Interface,
		Enable:     sub.Enable,
		URL:        sub.URL,
		Interval:   sub.Interval,
		LastUpdate: sub.LastUpdate,
	}
	if withRules {
		res.SubscriptionRulesRes = RespFromSubscriptionRules(sub.Rules)
	}
	return res
}

func RespFromSubscriptionRules(rules []*models.SubscriptionRule) types.SubscriptionRulesRes {
	list := make([]types.SubscriptionRuleRes, len(rules))
	for i, rule := range rules {
		list[i] = RespFromSubscriptionRule(rule)
	}
	return types.SubscriptionRulesRes{Rules: &list}
}

func RespFromSubscriptionRule(rule *models.SubscriptionRule) types.SubscriptionRuleRes {
	return types.SubscriptionRuleRes{
		ID:     rule.ID,
		Rule:   rule.Rule,
		Type:   rule.Type,
		Enable: rule.Enable,
	}
}
