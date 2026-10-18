package types

import "magitrickle/utils/intID"

type RoutingStatusRes struct {
	Suspended []RoutingRestoreStatus `json:"suspended"`
}

type RoutingRestoreStatus struct {
	ID                intID.ID `json:"id" swaggertype:"string"`
	Name              string   `json:"name"`
	Subscription      bool     `json:"subscription"`
	Error             string   `json:"error"`
	NextRetryAt       int64    `json:"nextRetryAt"`
	RetryDelaySeconds uint32   `json:"retryDelaySeconds"`
}
