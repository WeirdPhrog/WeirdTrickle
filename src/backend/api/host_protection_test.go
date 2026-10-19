package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"magitrickle/models"
)

func TestHTTPHostProtection(t *testing.T) {
	config := models.AppConfigHTTPWeb{AllowedHosts: []string{"router.example", "пример.рф"}}
	for _, test := range []struct {
		host    string
		allowed bool
	}{
		{"localhost:8080", true}, {"127.0.0.1:8080", true}, {"[::1]:8080", true},
		{"192.0.2.1:8080", true}, {"[2001:db8::1]:8080", true},
		{"ROUTER.EXAMPLE.:8443", true}, {"xn--e1afmkfd.xn--p1ai", true},
		{"evil.example:8080", false}, {"evil.router.example:8080", false},
		{"router.example.evil:8080", false}, {"192.0.2.2:8080", false},
		{"router.example@evil.example:8080", false}, {"router.example/path", false},
		{"router.example?token=1", false}, {"router.example#x", false},
		{"0.0.0.0:8080", false}, {"[::]:8080", false}, {"", false},
	} {
		t.Run(test.host, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/auth", nil)
			r.Host = test.host
			local := net.ParseIP("192.0.2.1")
			if strings.HasPrefix(test.host, "[") {
				local = net.ParseIP("2001:db8::1")
			}
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: local, Port: 8080}))
			if got := allowedHTTPHost(r, config); got != test.allowed {
				t.Fatalf("host=%s allowed=%t want=%t", test.host, got, test.allowed)
			}
		})
	}
}

func TestHTTPRejectsMatchingForeignHostAndOrigin(t *testing.T) {
	a := &browserTestApp{}
	r := httptest.NewRequest(http.MethodPost, "http://evil.example:8080/api/v1/system/config/save", strings.NewReader("{}"))
	r.Header.Set("Origin", "http://evil.example:8080")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	newHTTPHandler(a).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || a.saves != 0 {
		t.Fatalf("untrusted Host reached API: status=%d saves=%d", w.Code, a.saves)
	}
}
