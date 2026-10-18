package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"magitrickle"
	"magitrickle/api/v1"
	"magitrickle/api/v1/types"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

type routingStatusApp struct {
	*magitrickle.App
	statuses []models.RoutingRestoreStatus
}

func (a *routingStatusApp) RoutingStatus() []models.RoutingRestoreStatus { return a.statuses }

func TestRoutingStatusAPI(t *testing.T) {
	a := &routingStatusApp{App: &magitrickle.App{}, statuses: []models.RoutingRestoreStatus{}}
	router := v1.NewRouter(a)
	for _, statuses := range [][]models.RoutingRestoreStatus{
		{},
		{{ID: intID.ID{1}, Name: "Group", Error: "kernel busy", NextRetryAt: 1800000060, RetryDelaySeconds: 60},
			{ID: intID.ID{1}, Name: "Subscription", Subscription: true, Error: "not enough memory", NextRetryAt: 1800000900, RetryDelaySeconds: 900}},
	} {
		a.statuses = statuses
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/system/routing", nil))
		var response types.RoutingStatusRes
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || response.Suspended == nil || len(response.Suspended) != len(statuses) {
			t.Fatalf("invalid routing status response: %s, %v", w.Body.String(), err)
		}
		if len(statuses) > 0 && (response.Suspended[1].NextRetryAt != statuses[1].NextRetryAt || !response.Suspended[1].Subscription) {
			t.Fatal("API lost the subscription identity or next retry time")
		}
	}
}
