package subscriptions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestFetchAllStandardRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/list" {
					_, _ = w.Write([]byte("example.com\n"))
					return
				}
				http.Redirect(w, r, "/list", status)
			}))
			defer server.Close()
			body, err := fetchTestList(server.URL)
			if err != nil || body != "example.com\n" {
				t.Fatalf("redirect %d: body=%q err=%v", status, body, err)
			}
		})
	}
}

func TestFetchCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchListWithInterface(ctx, "http://127.0.0.1:1/list", ""); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
}
