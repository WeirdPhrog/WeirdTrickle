package v1

import (
	"net/http"

	"magitrickle/api/utils"
	"magitrickle/api/v1/types"
	"magitrickle/subscriptions"
)

// GetSubscriptionSettings
//
// @Summary Получить общий интерфейс загрузки подписок
// @Description Пустой fetchInterface означает системный маршрут. Настройка относится ко всем загрузкам URL подписок.
// @Tags subscriptions
// @Produce json
// @Success 200 {object} types.SubscriptionSettingsRes
// @Router /api/v1/subscriptions/settings [get]
func (h *Handler) GetSubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	utils.WriteJson(w, http.StatusOK, types.SubscriptionSettingsRes{FetchInterface: h.app.Config().SubscriptionFetchInterface})
}

// PutSubscriptionSettings
//
// @Summary Сохранить общий интерфейс загрузки подписок
// @Description fetchInterface обязателен; пустая строка выбирает системный маршрут. Настройка сохраняется сразу. При ошибке загрузки через выбранный интерфейс выполняется повтор через системный маршрут.
// @Tags subscriptions
// @Accept json
// @Produce json
// @Param json body types.SubscriptionSettingsReq true "Интерфейс загрузки"
// @Success 200 {object} types.SubscriptionSettingsRes
// @Failure 400 {object} types.ErrorRes
// @Failure 500 {object} types.ErrorRes
// @Router /api/v1/subscriptions/settings [put]
func (h *Handler) PutSubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.SubscriptionSettingsReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.FetchInterface == nil {
		utils.WriteError(w, http.StatusBadRequest, "fetchInterface is required")
		return
	}
	if err := subscriptions.ValidateFetchInterface(*req.FetchInterface); err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.app.SetSubscriptionFetchInterface(*req.FetchInterface); err != nil {
		utils.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteJson(w, http.StatusOK, types.SubscriptionSettingsRes{FetchInterface: *req.FetchInterface})
}
