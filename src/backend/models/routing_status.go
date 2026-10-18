package models

import "magitrickle/utils/intID"

// RoutingRestoreStatus is runtime state only; it is never persisted in config.
type RoutingRestoreStatus struct {
	ID                intID.ID `json:"id" swaggertype:"string"`
	Name              string   `json:"name"`
	Subscription      bool     `json:"subscription"`
	Error             string   `json:"error"`
	NextRetryAt       int64    `json:"nextRetryAt"`
	RetryDelaySeconds uint32   `json:"retryDelaySeconds"`
}
