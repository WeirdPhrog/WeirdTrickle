package v1

import (
	"net/http"

	"magitrickle/api/utils"
	"magitrickle/api/v1/types"
)

// GetRoutingStatus
//
// @Summary Get suspended routing sets and recovery status
// @Description Runtime rollback failures only; does not report general VPN health. nextRetryAt is the earliest automatic retry time (Unix seconds).
// @Tags system
// @Produce json
// @Success 200 {object} types.RoutingStatusRes
// @Router /api/v1/system/routing [get]
func (h *Handler) GetRoutingStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	statuses := h.app.RoutingStatus()
	response := types.RoutingStatusRes{Suspended: make([]types.RoutingRestoreStatus, len(statuses))}
	for i, status := range statuses {
		response.Suspended[i] = types.RoutingRestoreStatus{
			ID: status.ID, Name: status.Name, Subscription: status.Subscription,
			Error: status.Error, NextRetryAt: status.NextRetryAt, RetryDelaySeconds: status.RetryDelaySeconds,
		}
	}
	utils.WriteJson(w, http.StatusOK, response)
}
