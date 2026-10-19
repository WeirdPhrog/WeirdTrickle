package magitrickle

import (
	"context"
	"time"

	"magitrickle/app"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func (a *App) UpdateGroup(model *models.Group) error {
	return a.updateGroup(model, nil)
}

func (a *App) SyncSubscriptionByID(id intID.ID, now time.Time, urlOverride string) (app.SubscriptionSyncResult, bool, error) {
	return a.SyncSubscriptionByIDContext(context.Background(), id, now, urlOverride)
}

func (a *App) SyncDueSubscriptions(now time.Time) (bool, error) {
	return a.syncDueSubscriptions(context.Background(), now)
}
