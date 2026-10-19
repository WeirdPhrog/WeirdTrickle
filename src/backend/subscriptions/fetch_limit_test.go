package subscriptions

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFetchRejectsOversizedListsWithoutReturningPartialData(t *testing.T) {
	for _, mode := range []string{"content-length", "chunked", "gzip"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out io.Writer = w
				switch mode {
				case "content-length":
					w.Header().Set("Content-Length", strconv.Itoa(maxListBytes+1))
					return
				case "chunked":
					w.(http.Flusher).Flush()
				case "gzip":
					w.Header().Set("Content-Encoding", "gzip")
					gz := gzip.NewWriter(w)
					defer gz.Close()
					out = gz
				}
				block := strings.Repeat("a", 4096)
				for written := 0; written <= maxListBytes; written += len(block) {
					if _, err := io.WriteString(out, block); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			body, err := fetchTestList(server.URL)
			if err == nil || !strings.Contains(err.Error(), "16 MiB") || body != "" {
				t.Fatalf("body length=%d err=%v", len(body), err)
			}
		})
	}
}
