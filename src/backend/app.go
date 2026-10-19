package magitrickle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"magitrickle/app"
	"magitrickle/constant"
	groupruntime "magitrickle/groups"
	"magitrickle/internal/interfaces"
	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/dnsMITMProxy"
	"magitrickle/utils/intID"
	"magitrickle/utils/netfilterTools"
	"magitrickle/utils/recordsCache"

	"github.com/rs/zerolog/log"
)

var (
	ErrAlreadyRunning           = errors.New("already running")
	ErrGroupIDConflict          = errors.New("group id conflict")
	ErrRuleIDConflict           = errors.New("rule id conflict")
	ErrConfigUnsupportedVersion = errors.New("config unsupported version")
)

// App – основная структура ядра приложения
type App struct {
	enabled atomic.Bool

	config                 models.AppConfig
	configSnapshot         atomic.Pointer[models.AppConfig]
	dnsRules               atomic.Pointer[dnsRuleSnapshot]
	configDNSRules         []rulesets.Spec // stateMu: pending full-config transaction
	routingRestoresPending atomic.Bool
	stateMu                sync.RWMutex
	configFileMu           sync.Mutex
	configLoadErr          error

	subscriptionSyncMu  sync.Mutex
	subscriptionRetries map[intID.ID]subscriptionRetry
	subscriptionFetch   func(context.Context, string, string) (string, error) // immutable dependency; nil uses the protected HTTP downloader

	dnsMITM              *dnsMITMProxy.DNSMITMProxy
	nfHelper             *netfilterTools.Helper
	recordsCache         *recordsCache.Records
	userRuleSets         []*RuleSet
	subscriptionRuleSets []*RuleSet
	dnsOverrider         *netfilterTools.PortRemap
	subscriptions        []*models.Subscription
}

// New создаёт новый экземпляр App
func New() *App {
	a := &App{
		config: constant.DefaultAppConfig,
	}
	a.publishConfigLocked()
	if err := a.LoadConfig(); err != nil {
		log.Error().Err(err).Msg("failed to load config file")
	}
	return a
}

// Config возвращает конфигурацию
func (a *App) Config() models.AppConfig {
	if config := a.configSnapshot.Load(); config != nil {
		return *config
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.config
}

// The copy and its referenced values are immutable after publication.
func (a *App) publishConfigLocked() {
	config := a.config
	a.configSnapshot.Store(&config)
}

// UserGroups returns only user-defined groups.
func (a *App) UserGroups() []app.RuleSet {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	list := make([]app.RuleSet, len(a.userRuleSets))
	for i, g := range a.userRuleSets {
		list[i] = g
	}
	return list
}

// AddGroup добавляет новую группу
func (a *App) AddGroup(groupModel *models.Group) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.addGroupLocked(groupModel)
}

func (a *App) addGroupLocked(groupModel *models.Group) error {
	if err := validateGroups([]*models.Group{groupModel}); err != nil {
		return err
	}
	for _, group := range a.userRuleSets {
		if groupModel.ID == group.IDValue() {
			return ErrGroupIDConflict
		}
	}
	grp := newRuleSet(groupruntime.BuildRuntimeRuleSet(groupModel), a)
	a.userRuleSets = append(a.userRuleSets, grp)
	a.publishDNSRulesLocked(nil)
	defer a.publishDNSRulesLocked(nil)
	removeAdded := func() {
		a.userRuleSets = a.userRuleSets[:len(a.userRuleSets)-1]
	}

	log.Info().
		Str("id", grp.IDValue().String()).
		Str("name", grp.DisplayName()).
		Msg("added group")

	// если приложение уже запущено – включаем группу и выполняем синхронизацию
	if a.enabled.Load() {
		if err := grp.Enable(); err != nil {
			removeAdded()
			return fmt.Errorf("failed to enable group: %w", err)
		}
	}
	return nil
}

// RemoveGroupByID removes a group by ID.
func (a *App) RemoveGroupByID(id intID.ID) (bool, error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	defer a.publishDNSRulesLocked(nil)
	for idx, group := range a.userRuleSets {
		if group.IDValue() == id {
			if err := group.Disable(); err != nil {
				return false, err
			}
			a.userRuleSets = append(a.userRuleSets[:idx], a.userRuleSets[idx+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// WithSubscriptions invokes fn while holding the state read lock so the slice
// passed in is a stable snapshot of the live subscriptions. Callers must treat
// the slice and its elements as read-only and must not retain references past
// the callback.
func (a *App) WithSubscriptions(fn func([]*models.Subscription)) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	fn(a.subscriptions)
}

// ReplaceSubscriptions replaces the subscriptions list and rebuilds subscription rule sets.
func (a *App) ReplaceSubscriptions(subscriptions []*models.Subscription) error {
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.setSubscriptionsLocked(subscriptions)
}

// Caller holds subscriptionSyncMu and stateMu. next must not share its backing
// array with the current list, so the current list remains a valid rollback.
func (a *App) setSubscriptionsLocked(next []*models.Subscription) error {
	previous := a.subscriptions
	a.subscriptions = next
	if err := a.syncSubscriptionRuleSetsLocked(); err != nil {
		a.subscriptions = previous
		if rollbackErr := a.syncSubscriptionRuleSetsLocked(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("failed to rollback subscriptions: %w", rollbackErr))
		}
		return err
	}
	return nil
}

// AddSubscription adds a new subscription if ID is unique and rebuilds subscription rule sets.
func (a *App) AddSubscription(subscription *models.Subscription) error {
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	for _, sub := range a.subscriptions {
		if sub.ID == subscription.ID {
			return app.ErrSubscriptionConflict
		}
	}
	return a.setSubscriptionsLocked(append(slices.Clip(a.subscriptions), subscription))
}

// RemoveSubscriptionByID removes a subscription by ID.
func (a *App) RemoveSubscriptionByID(id intID.ID) (bool, error) {
	a.subscriptionSyncMu.Lock()
	defer a.subscriptionSyncMu.Unlock()

	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	for idx, sub := range a.subscriptions {
		if sub.ID == id {
			if err := a.setSubscriptionsLocked(slices.Delete(slices.Clone(a.subscriptions), idx, idx+1)); err != nil {
				return true, err
			}
			delete(a.subscriptionRetries, id)
			return true, nil
		}
	}
	return false, nil
}

// ListInterfaces возвращает список сетевых интерфейсов, доступных для выбора в UI.
func (a *App) ListInterfaces() ([]models.InterfaceInfo, error) {
	return interfaces.List(a.Config().ShowAllInterfaces)
}

func (a *App) ruleSetSnapshot() []*RuleSet {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.ruleSetSnapshotLocked()
}

func (a *App) ruleSetSnapshotLocked() []*RuleSet {
	list := make([]*RuleSet, 0, len(a.userRuleSets)+len(a.subscriptionRuleSets))
	list = append(list, a.userRuleSets...)
	list = append(list, a.subscriptionRuleSets...)
	return list
}
