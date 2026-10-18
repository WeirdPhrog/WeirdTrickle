package subscriptions

import (
	"fmt"
	"time"

	"magitrickle/models"
)

func IsDue(sub *models.Subscription, now time.Time) bool {
	if sub == nil || !sub.Enable || sub.URL == "" || sub.Interval == 0 {
		return false
	}
	lastCheck := sub.LastCheck
	if lastCheck == 0 {
		lastCheck = sub.LastUpdate
	}
	if lastCheck > 0 && uint64(now.Unix()) < uint64(lastCheck)+uint64(sub.Interval) {
		return false
	}
	return true
}

func PlanRefresh(currentRules []*models.SubscriptionRule, list string) (refreshed []*models.SubscriptionRule, changed bool, err error) {
	refreshed, err = RefreshRules(list, currentRules)
	if err != nil {
		return nil, false, err
	}
	if len(refreshed) == 0 && len(currentRules) != 0 {
		return nil, false, fmt.Errorf("empty subscription response; retaining the previous rules")
	}
	if err := ValidateRules(refreshed); err != nil {
		return nil, false, err
	}
	changed = !sameRules(currentRules, refreshed)
	return refreshed, changed, nil
}
