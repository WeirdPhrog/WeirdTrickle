package app

import (
	"context"
	"errors"
	"time"

	"magitrickle/models"
	"magitrickle/utils/intID"
)

var (
	ErrGroupNotFound        = errors.New("group not found")
	ErrGroupChanged         = errors.New("group changed during the request; reload and retry")
	ErrSubscriptionConflict = errors.New("subscription id conflict")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrSubscriptionInvalid  = errors.New("subscription invalid")
	ErrSubscriptionFetch    = errors.New("subscription fetch failed")
)

type SubscriptionSyncResult struct {
	URL        string
	LastUpdate uint32
	Rules      []*models.SubscriptionRule
}

// Main is the application surface used by the HTTP and Unix-socket API.
type Main interface {
	Config() models.AppConfig
	RoutingStatus() []models.RoutingRestoreStatus
	UserGroups() []RuleSet
	AddGroup(groupModel *models.Group) error
	UpdateGroupIfCurrent(groupModel, expected *models.Group) error
	ReplaceGroups(groups []*models.Group) error
	RemoveGroupByID(id intID.ID) (bool, error)
	WithSubscriptions(fn func([]*models.Subscription))
	ReplaceSubscriptions(subscriptions []*models.Subscription) error
	AddSubscription(subscription *models.Subscription) error
	RemoveSubscriptionByID(id intID.ID) (bool, error)
	SyncSubscriptionByIDContext(ctx context.Context, id intID.ID, now time.Time, urlOverride string) (result SubscriptionSyncResult, changed bool, err error)
	FetchSubscriptionList(ctx context.Context, url string) (string, error)
	SetSubscriptionFetchInterface(name string) error
	ListInterfaces() ([]models.InterfaceInfo, error)
	SaveConfig() error
	ForceCommitIPTables() error
}

// RuleSet is the read-only view of a user group exposed to the API.
type RuleSet interface {
	Model() *models.Group
}
