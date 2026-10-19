package subscriptions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestFetchFallbackOrder(t *testing.T) {
	failure := errors.New("unavailable")
	for _, tc := range []struct {
		name                               string
		iface                              string
		failPreferred, failDefault, cancel bool
		wantCalls                          []string
	}{
		{name: "default", wantCalls: []string{""}},
		{name: "preferred succeeds", iface: "wg0", wantCalls: []string{"wg0"}},
		{name: "fallback", iface: "wg0", failPreferred: true, wantCalls: []string{"wg0", ""}},
		{name: "both fail", iface: "wg0", failPreferred: true, failDefault: true, wantCalls: []string{"wg0", ""}},
		{name: "cancel stops fallback", iface: "wg0", failPreferred: true, cancel: true, wantCalls: []string{"wg0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			list, err := fetchWithFallback(ctx, tc.iface, func(_ context.Context, name string) (string, error) {
				calls = append(calls, name)
				if tc.cancel {
					cancel()
				}
				if name != "" && tc.failPreferred || name == "" && tc.failDefault {
					return "partial list must not replace rules", failure
				}
				return "example.com\n", nil
			})
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tc.wantCalls)
			}
			if tc.failDefault || tc.cancel {
				if err == nil || list != "" {
					t.Fatalf("failed download returned %q, %v", list, err)
				}
			} else if err != nil || list != "example.com\n" {
				t.Fatalf("list = %q, err = %v", list, err)
			}
		})
	}
}

func TestFetchMissingInterfaceRetriesOriginalURL(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/list", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer server.Close()
	list, err := fetchListWithPolicy(context.Background(), server.URL+"/start", "mt-no-such-if", func(string) error { return nil })
	mu.Lock()
	defer mu.Unlock()
	if err != nil || list != "example.com\n" || !reflect.DeepEqual(paths, []string{"/start", "/list"}) {
		t.Fatalf("list=%q err=%v paths=%v", list, err, paths)
	}
}

func TestFetchInterfaceValidation(t *testing.T) {
	for _, name := range []string{"", "nwg0", "tun0", "ppp0", "eth0.100", "veth-1", "lo"} {
		if err := ValidateFetchInterface(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{".", "..", "longinterface123", "wg0\x00", " wg0", "wg0/1", "wg0:1", "wg0\n"} {
		if err := ValidateFetchInterface(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestLocalResolverDetection(t *testing.T) {
	local := []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.1"), Mask: net.CIDRMask(24, 32)}}
	for _, address := range []string{"127.0.0.1:53", "[::1]:53", "192.168.1.1:53"} {
		if !isLocalResolver(address, local) {
			t.Errorf("local resolver %s not recognized", address)
		}
	}
	for _, address := range []string{"192.168.1.2:53", "1.1.1.1:53", "[2606:4700:4700::1111]:53"} {
		if isLocalResolver(address, local) {
			t.Errorf("remote resolver %s treated as local", address)
		}
	}
}
