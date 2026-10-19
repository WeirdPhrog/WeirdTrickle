package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"magitrickle/app"
	"magitrickle/models"
)

type browserTestApp struct {
	app.Main
	saves  int
	config models.AppConfig
}

func (a *browserTestApp) Config() models.AppConfig {
	config := a.config
	config.HTTPWeb.AllowedHosts = append([]string{"router.test"}, config.HTTPWeb.AllowedHosts...)
	return config
}
func (a *browserTestApp) SaveConfig() error { a.saves++; return nil }

func TestHTTPRejectsForeignBrowserRequests(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site string
		allowed            bool
	}{
		{"local CLI", "", "", true},
		{"same origin", "http://router.test:8080", "same-origin", true},
		{"TLS reverse proxy", "https://router.test:8080", "same-origin", true},
		{"foreign form", "https://foreign.example", "", false},
		{"foreign fetch", "https://foreign.example", "cross-site", false},
		{"opaque origin", "null", "", false},
		{"different port", "http://router.test:8081", "same-site", false},
		{"subdomain", "http://evil.router.test:8080", "same-site", false},
		{"userinfo", "http://evil@router.test:8080", "", false},
		{"malformed origin", "://", "", false},
		{"cross site without origin", "", "cross-site", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &browserTestApp{}
			r := httptest.NewRequest(http.MethodPost, "http://router.test:8080/api/v1/system/config/save", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "text/plain") // A simple request needs no CORS preflight.
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			newHTTPHandler(a).ServeHTTP(w, r)
			wantStatus, wantSaves := http.StatusForbidden, 0
			if tc.allowed {
				wantStatus, wantSaves = http.StatusOK, 1
			}
			if w.Code != wantStatus || a.saves != wantSaves {
				t.Fatalf("status=%d saves=%d; want %d/%d", w.Code, a.saves, wantStatus, wantSaves)
			}
			if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
				t.Fatal("missing frame protection")
			}
		})
	}
}

func TestHTTPOriginChecksDoNotBypassAuthentication(t *testing.T) {
	a := &browserTestApp{}
	a.config.HTTPWeb.Auth.Enabled = true
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://router.test/api/v1/system/config/save", nil)
	r.Header.Set("Origin", "http://router.test")
	newHTTPHandler(a).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || a.saves != 0 {
		t.Fatalf("status=%d saves=%d", w.Code, a.saves)
	}
}

func TestHTTPLoginRejectsOversizedCredentials(t *testing.T) {
	a := &browserTestApp{}
	a.config.HTTPWeb.Auth.Enabled = true
	for _, size := range []int{1025, 9000} {
		r := httptest.NewRequest(http.MethodPost, "http://router.test/api/v1/auth", strings.NewReader(`{"login":"root","password":"`+strings.Repeat("a", size)+`"}`))
		w := httptest.NewRecorder()
		newHTTPHandler(a).ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("password length %d returned %d: %s", size, w.Code, w.Body.String())
		}
	}
}
