package auth

import (
	"net/http"

	"magitrickle/api/utils"
	"magitrickle/app"
)

type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type StatusResponse struct {
	Enabled bool `json:"enabled"`
}

// StatusHandler
//
// @Summary Проверить, включена ли авторизация
// @Tags auth
// @Produce json
// @Success 200 {object} StatusResponse
// @Router /api/v1/auth [get]
func StatusHandler(app app.Main) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		utils.WriteJson(w, http.StatusOK, StatusResponse{Enabled: app.Config().HTTPWeb.Auth.Enabled})
	}
}

// LoginHandler
//
// @Summary Получить токен авторизации
// @Description Возвращает токен для заголовка Authorization: Bearer <token>.
// @Tags auth
// @Accept json
// @Produce json
// @Param json body LoginRequest true "Логин и пароль"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} map[string]string "Некорректные данные"
// @Failure 403 {object} map[string]string "Неверные учётные данные"
// @Failure 404 {object} map[string]string "Авторизация отключена"
// @Router /api/v1/auth [post]
func LoginHandler(app app.Main) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.Config().HTTPWeb.Auth.Enabled {
			utils.WriteError(w, http.StatusNotFound, "Auth disabled")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		req, err := utils.ReadJson[LoginRequest](r)
		if err != nil {
			utils.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Login == "" || req.Password == "" {
			utils.WriteError(w, http.StatusBadRequest, "missing credentials")
			return
		}
		if len(req.Login) > maxLoginBytes || len(req.Password) > maxPasswordBytes {
			utils.WriteError(w, http.StatusBadRequest, "credentials too long")
			return
		}

		token, err := Authenticate(req.Login, req.Password)
		if err != nil {
			utils.WriteError(w, http.StatusForbidden, "Invalid credentials")
			return
		}
		utils.WriteJson(w, http.StatusOK, LoginResponse{Token: token})
	}
}
