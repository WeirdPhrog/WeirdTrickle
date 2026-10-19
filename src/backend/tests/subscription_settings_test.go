package tests

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"magitrickle"
	"magitrickle/api/v1"
	"magitrickle/models"
)

type settingsApp struct {
	*magitrickle.App
	config  models.AppConfig
	saveErr error
}

func (a *settingsApp) Config() models.AppConfig { return a.config }
func (a *settingsApp) SetSubscriptionFetchInterface(name string) error {
	if a.saveErr != nil {
		return a.saveErr
	}
	a.config.SubscriptionFetchInterface = name
	return nil
}

func TestSubscriptionSettingsAPI(t *testing.T) {
	a := &settingsApp{App: &magitrickle.App{}}
	router := v1.NewRouter(a)
	request := func(method, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/subscriptions/settings", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		return w
	}
	if !strings.Contains(request(http.MethodGet, "", 200).Body.String(), `"fetchInterface":""`) {
		t.Fatal("wrong default")
	}
	for _, body := range []string{`{}`, `{"fetchInterface":null}`, `{"fetchInterface":1}`, `{"fetchInterface":"wg 0"}`} {
		request(http.MethodPut, body, 400)
	}
	request(http.MethodPut, `{"fetchInterface":"nwg1"}`, 200)
	if !strings.Contains(request(http.MethodGet, "", 200).Body.String(), `"fetchInterface":"nwg1"`) {
		t.Fatal("setting not applied")
	}
	a.saveErr = errors.New("disk full")
	request(http.MethodPut, `{"fetchInterface":"nwg2"}`, 500)
	if a.config.SubscriptionFetchInterface != "nwg1" {
		t.Fatal("failed save changed setting")
	}
	a.saveErr = nil
	request(http.MethodPut, `{"fetchInterface":""}`, 200)
	if a.config.SubscriptionFetchInterface != "" {
		t.Fatal("system default not restored")
	}
}

func TestSubscriptionPreviewRejectsRouterLocalURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer server.Close()
	router := v1.NewRouter(&magitrickle.App{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/subscriptions/rules?url="+server.URL, nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), "example.com") || !strings.Contains(w.Body.String(), "not allowed") {
		t.Fatal(w.Code, w.Body.String())
	}
}
