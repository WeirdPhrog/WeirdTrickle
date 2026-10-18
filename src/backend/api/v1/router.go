package v1

import (
	"context"
	"magitrickle/models"
	"net/http"
	"strconv"

	"magitrickle/api/auth"
	"magitrickle/api/utils"
	"magitrickle/app"
	"magitrickle/utils/intID"

	"github.com/go-chi/chi/v5"
)

type groupContextKey struct{}

func requestGroup(r *http.Request) *models.Group {
	return r.Context().Value(groupContextKey{}).(*models.Group)
}

// NewRouter собирает маршруты API v1
func NewRouter(a app.Main) chi.Router {
	h := NewHandler(a)
	r := chi.NewRouter()
	r.Get("/auth", auth.StatusHandler(a))
	r.Post("/auth", auth.LoginHandler(a))
	r.Route("/groups", func(r chi.Router) {
		r.Get("/", h.GetGroups)
		r.Put("/", h.PutGroups)
		r.Post("/", h.CreateGroup)
		r.Route("/{groupID}", func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					groupID := chi.URLParam(r, "groupID")
					id, err := intID.ParseID(groupID)
					if err != nil {
						utils.WriteError(w, http.StatusBadRequest, "invalid group id")
						return
					}
					for _, group := range h.userGroups() {
						if group.Model().ID == id {
							next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), groupContextKey{}, group.Model())))
							return
						}
					}
					utils.WriteError(w, http.StatusNotFound, "group not exist")
				})
			})
			r.Get("/", h.GetGroup)
			r.Put("/", h.PutGroup)
			r.Delete("/", h.DeleteGroup)
			r.Route("/rules", func(r chi.Router) {
				r.Get("/", h.GetRules)
				r.Put("/", h.PutRules)
				r.Post("/", h.CreateRule)
				r.Route("/{ruleID}", func(r chi.Router) {
					r.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							ruleID := chi.URLParam(r, "ruleID")
							id, err := intID.ParseID(ruleID)
							if err != nil {
								utils.WriteError(w, http.StatusBadRequest, "invalid rule id")
								return
							}
							for idx, rule := range requestGroup(r).Rules {
								if rule.ID == id {
									r.Header.Set("ruleIdx", strconv.Itoa(idx))
									next.ServeHTTP(w, r)
									return
								}
							}
							utils.WriteError(w, http.StatusNotFound, "rule not exist")
						})
					})
					r.Get("/", h.GetRule)
					r.Put("/", h.PutRule)
					r.Delete("/", h.DeleteRule)
				})
			})
		})
	})
	r.Route("/subscriptions", func(r chi.Router) {
		r.Get("/", h.GetSubscriptions)
		r.Put("/", h.PutSubscriptions)
		r.Post("/", h.CreateSubscription)
		r.Get("/rules", h.GetSubscriptionRules)
		r.Get("/settings", h.GetSubscriptionSettings)
		r.Put("/settings", h.PutSubscriptionSettings)
		r.Route("/{subscriptionID}", func(r chi.Router) {
			r.Post("/sync", h.SyncSubscription)
			r.Delete("/", h.DeleteSubscription)
		})
	})
	r.Route("/system", func(r chi.Router) {
		r.Get("/routing", h.GetRoutingStatus)
		r.Get("/interfaces", h.ListInterfaces)
		r.Route("/config", func(r chi.Router) {
			r.Post("/save", h.SaveConfig)
		})
		r.Route("/hooks", func(r chi.Router) {
			r.Post("/netfilterd", h.NetfilterDHook)
		})
	})
	return r
}
