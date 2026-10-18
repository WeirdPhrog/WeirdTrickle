package v1

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"magitrickle/api/utils"
	"magitrickle/api/v1/types"
	"magitrickle/app"
	"magitrickle/models"
	"magitrickle/utils/intID"

	"github.com/rs/zerolog/log"
)

// Handler предоставляет набор методов для обработки API запросов.
type Handler struct {
	app app.Main
}

// NewHandler создаёт новый обработчик для API v1.
func NewHandler(a app.Main) *Handler {
	return &Handler{app: a}
}

func (h *Handler) userGroups() []app.RuleSet {
	return h.app.UserGroups()
}

// NetfilterDHook
//
//	@Summary		Хук эвента netfilter.d
//	@Description	Эмитирует хук эвента netfilter.d
//	@Tags			hooks
//	@Accept			json
//	@Produce		json
//	@Param			json	body	types.NetfilterDHookReq	true	"Тело запроса"
//	@Success		200
//	@Failure		400	{object}	types.ErrorRes
//	@Failure		500	{object}	types.ErrorRes
//	@Router			/api/v1/system/hooks/netfilterd [post]
func (h *Handler) NetfilterDHook(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.NetfilterDHookReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Debug().
		Str("type", req.Type).
		Str("table", req.Table).
		Msg("received netfilter.d event")
	err = h.app.ForceCommitIPTables()
	if err != nil {
		log.Error().Err(err).Msg("error fixing iptables after netfilter.d")
	}
}

// ListInterfaces
//
//	@Summary		Получить список интерфейсов
//	@Description	Возвращает список интерфейсов
//	@Tags			config
//	@Produce		json
//	@Success		200	{object}	types.InterfacesRes
//	@Failure		500	{object}	types.ErrorRes
//	@Router			/api/v1/system/interfaces [get]
func (h *Handler) ListInterfaces(w http.ResponseWriter, r *http.Request) {
	interfaces, err := h.app.ListInterfaces()
	if err != nil {
		utils.WriteError(w, http.StatusInternalServerError, fmt.Errorf("failed to get interfaces: %w", err).Error())
		return
	}

	res := make([]types.InterfaceRes, len(interfaces)+1)
	res[0] = types.InterfaceRes{ID: "blackhole"}
	for i, iface := range interfaces {
		res[i+1] = types.InterfaceRes{
			ID:   iface.ID,
			Name: iface.Name,
		}
	}

	utils.WriteJson(w, http.StatusOK, types.InterfacesRes{Interfaces: res})
}

// SaveConfig
//
//	@Summary		Сохранить конфигурацию
//	@Description	Сохраняет текущую конфигурацию в постоянную память
//	@Tags			config
//	@Produce		json
//	@Success		200
//	@Failure		500	{object}	types.ErrorRes
//	@Router			/api/v1/system/config/save [post]
func (h *Handler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.app.SaveConfig(); err != nil {
		utils.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("changes are applied, but failed to save config: %v", err))
	}
}

// GetGroups
//
//	@Summary		Получить список групп
//	@Description	Возвращает список групп
//	@Tags			groups
//	@Produce		json
//	@Param			with_rules	query		bool	false	"Возвращать группы с их правилами"
//	@Success		200			{object}	types.GroupsRes
//	@Failure		500			{object}	types.ErrorRes
//	@Router			/api/v1/groups [get]
func (h *Handler) GetGroups(w http.ResponseWriter, r *http.Request) {
	withRules := r.URL.Query().Get("with_rules") == "true"
	appGroups := h.userGroups()
	modelGroups := make([]*models.Group, len(appGroups))
	for i, g := range appGroups {
		modelGroups[i] = g.Model()
	}
	utils.WriteJson(w, http.StatusOK, RespFromGroups(modelGroups, withRules))
}

// PutGroups
//
//	@Summary		Обновить список групп
//	@Description	Обновляет список групп
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.GroupsReq	true	"Тело запроса"
//	@Success		200		{object}	types.GroupsRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups [put]
func (h *Handler) PutGroups(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.GroupsReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Groups == nil {
		utils.WriteError(w, http.StatusBadRequest, "no groups in request")
		return
	}
	existing := make(map[intID.ID]*models.Group)
	for _, group := range h.userGroups() {
		existing[group.Model().ID] = group.Model()
	}
	newGroups := make([]*models.Group, len(*req.Groups))
	ids := make(map[intID.ID]bool)
	for i, req := range *req.Groups {
		var previous *models.Group
		if req.ID != nil {
			previous = existing[*req.ID]
		}
		group, err := GroupFromReq(req, previous)
		if err != nil {
			utils.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if ids[group.ID] {
			utils.WriteError(w, http.StatusBadRequest, "duplicate group ID")
			return
		}
		ids[group.ID] = true
		newGroups[i] = group
	}
	if err := h.app.ReplaceGroups(newGroups); err != nil {
		utils.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromGroups(newGroups, true))
}

// CreateGroup
//
//	@Summary		Создать группу
//	@Description	Создает группу
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.GroupReq	true	"Тело запроса"
//	@Success		200		{object}	types.GroupRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups [post]
func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.GroupReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	group, err := GroupFromReq(req, nil)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.app.AddGroup(group); err != nil {
		utils.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromGroup(group, true))
}

// GetGroup
//
//	@Summary		Получить группу
//	@Description	Возвращает запрошенную группу
//	@Tags			groups
//	@Produce		json
//	@Param			groupID		path		string	true	"ID группы"
//	@Param			with_rules	query		bool	false	"Возвращать группу с её правилами"
//	@Success		200			{object}	types.GroupRes
//	@Failure		404			{object}	types.ErrorRes
//	@Failure		500			{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID} [get]
func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	utils.WriteJson(w, http.StatusOK, RespFromGroup(requestGroup(r), r.URL.Query().Get("with_rules") == "true"))
}

// PutGroup
//
//	@Summary		Обновить группу
//	@Description	Обновляет запрошенную группу
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Param			groupID	path		string			true	"ID группы"
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.GroupReq	true	"Тело запроса"
//	@Success		200		{object}	types.GroupRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID} [put]
func (h *Handler) PutGroup(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.GroupReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := GroupFromReq(req, requestGroup(r))
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.updateGroup(w, r, updated) || !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromGroup(updated, true))
}

// DeleteGroup
//
//	@Summary		Удалить группу
//	@Description	Удаляет запрошенную группу
//	@Tags			groups
//	@Produce		json
//	@Param			groupID	path	string	true	"ID группы"
//	@Param			save	query	bool	false	"Сохранить изменения в конфигурационный файл"
//	@Success		200
//	@Failure		404	{object}	types.ErrorRes
//	@Failure		500	{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID} [delete]
func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	removed, err := h.app.RemoveGroupByID(requestGroup(r).ID)
	if err != nil {
		utils.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !removed {
		utils.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	if !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetRules
//
//	@Summary		Получить список правил
//	@Description	Возвращает список правил
//	@Tags			rules
//	@Produce		json
//	@Param			groupID	path		string	true	"ID группы"
//	@Success		200		{object}	types.RulesRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules [get]
func (h *Handler) GetRules(w http.ResponseWriter, r *http.Request) {
	utils.WriteJson(w, http.StatusOK, RespFromRules(requestGroup(r).Rules))
}

// PutRules
//
//	@Summary		Обновить список правил
//	@Description	Обновляет список правил
//	@Tags			rules
//	@Accept			json
//	@Produce		json
//	@Param			groupID	path		string			true	"ID группы"
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.RulesReq	true	"Тело запроса"
//	@Success		200		{object}	types.RulesRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules [put]
func (h *Handler) PutRules(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.RulesReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Rules == nil {
		utils.WriteError(w, http.StatusBadRequest, "no rules in request")
		return
	}
	updated := *requestGroup(r)
	existingIDs := indexRuleIDs(updated.Rules)
	newRules := make([]*models.Rule, len(*req.Rules))
	for i, rr := range *req.Rules {
		if rr.ID != nil {
			if _, found := existingIDs[*rr.ID]; !found {
				utils.WriteError(w, http.StatusNotFound, "rule not found")
				return
			}
		}
		newRules[i], err = ruleFromReq(rr, existingIDs)
		if err != nil {
			utils.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	updated.Rules = newRules
	if !h.updateGroup(w, r, &updated) || !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromRules(newRules))
}

// CreateRule
//
//	@Summary		Создать правило
//	@Description	Создает правило
//	@Tags			rules
//	@Accept			json
//	@Produce		json
//	@Param			groupID	path		string			true	"ID группы"
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.RuleReq	true	"Тело запроса"
//	@Success		200		{object}	types.RuleRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules [post]
func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.RuleReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated := *requestGroup(r)
	rule, err := RuleFromReq(req, updated.Rules)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated.Rules = append(append([]*models.Rule(nil), updated.Rules...), rule)
	if !h.updateGroup(w, r, &updated) || !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromRule(rule))
}

// GetRule
//
//	@Summary		Получить правило
//	@Description	Возвращает запрошенное правило
//	@Tags			rules
//	@Produce		json
//	@Param			groupID	path		string	true	"ID группы"
//	@Param			ruleID	path		string	true	"ID правила"
//	@Success		200		{object}	types.RuleRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules/{ruleID} [get]
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	idx, _ := strconv.Atoi(r.Header.Get("ruleIdx"))
	utils.WriteJson(w, http.StatusOK, RespFromRule(requestGroup(r).Rules[idx]))
}

// PutRule
//
//	@Summary		Обновить правило
//	@Description	Обновляет запрошенное правило
//	@Tags			rules
//	@Accept			json
//	@Produce		json
//	@Param			groupID	path		string			true	"ID группы"
//	@Param			ruleID	path		string			true	"ID правила"
//	@Param			save	query		bool			false	"Сохранить изменения в конфигурационный файл"
//	@Param			json	body		types.RuleReq	true	"Тело запроса"
//	@Success		200		{object}	types.RuleRes
//	@Failure		400		{object}	types.ErrorRes
//	@Failure		404		{object}	types.ErrorRes
//	@Failure		500		{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules/{ruleID} [put]
func (h *Handler) PutRule(w http.ResponseWriter, r *http.Request) {
	req, err := utils.ReadJson[types.RuleReq](r)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated := *requestGroup(r)
	idx, _ := strconv.Atoi(r.Header.Get("ruleIdx"))
	rule := &models.Rule{ID: updated.Rules[idx].ID, Name: req.Name, Type: req.Type, Rule: req.Rule, Enable: req.Enable}
	updated.Rules = append([]*models.Rule(nil), updated.Rules...)
	updated.Rules[idx] = rule
	if !h.updateGroup(w, r, &updated) || !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, RespFromRule(rule))
}

// DeleteRule
//
//	@Summary		Удалить правило
//	@Description	Удаляет запрошенное правило
//	@Tags			rules
//	@Produce		json
//	@Param			groupID	path	string	true	"ID группы"
//	@Param			ruleID	path	string	true	"ID правила"
//	@Param			save	query	bool	false	"Сохранить изменения в конфигурационный файл"
//	@Success		200
//	@Failure		404	{object}	types.ErrorRes
//	@Failure		500	{object}	types.ErrorRes
//	@Router			/api/v1/groups/{groupID}/rules/{ruleID} [delete]
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	updated := *requestGroup(r)
	idx, _ := strconv.Atoi(r.Header.Get("ruleIdx"))
	updated.Rules = append(append([]*models.Rule(nil), updated.Rules[:idx]...), updated.Rules[idx+1:]...)
	if !h.updateGroup(w, r, &updated) || !h.saveIfRequested(w, r, false) {
		return
	}
	utils.WriteJson(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request, group *models.Group) bool {
	if err := h.app.UpdateGroupIfCurrent(group, requestGroup(r)); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, app.ErrGroupNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, app.ErrGroupChanged) {
			status = http.StatusConflict
		}
		utils.WriteError(w, status, err.Error())
		return false
	}
	return true
}

func (h *Handler) saveIfRequested(w http.ResponseWriter, r *http.Request, defaultSave bool) bool {
	save := r.URL.Query().Get("save")
	if save != "true" && (!defaultSave || save == "false") {
		return true
	}
	if err := h.app.SaveConfig(); err != nil {
		utils.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("changes are applied, but failed to save config: %v", err))
		return false
	}
	return true
}
