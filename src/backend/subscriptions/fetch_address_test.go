package subscriptions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchAddressPolicy(t *testing.T) {
	local := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.1"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("203.0.113.5"), Mask: net.CIDRMask(32, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)},
	}
	for _, value := range []string{"127.0.0.1", "127.42.1.2", "::1", "::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3", "::", "169.254.169.254", "fe80::1%eth0", "ff02::1", "224.0.0.1", "255.255.255.255", "192.168.1.1", "::ffff:192.168.1.1", "203.0.113.5", "2001:db8::1"} {
		if err := validateFetchAddress(netip.MustParseAddr(value), local); !errors.Is(err, errForbiddenFetchAddress) {
			t.Errorf("allowed protected address %s: %v", value, err)
		}
	}
	for _, value := range []string{"1.1.1.1", "2606:4700:4700::1111", "192.168.1.20", "10.8.0.10", "fd00::20"} {
		if err := validateFetchAddress(netip.MustParseAddr(value), local); err != nil {
			t.Errorf("rejected separate subscription server %s: %v", value, err)
		}
	}
}

func TestFetchRejectsLocalTargetsBeforeSendingHTTPRequest(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("private.example\n"))
	}))
	defer server.Close()
	for _, target := range []string{server.URL, strings.Replace(server.URL, "127.0.0.1", "localhost", 1)} {
		body, err := FetchList(target + "/?token=PRIVATE_TOKEN")
		if !errors.Is(err, errForbiddenFetchAddress) || body != "" || strings.Contains(err.Error(), "PRIVATE_TOKEN") {
			t.Fatalf("local fetch: body=%q err=%v", body, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("request reached local service")
	}
}

func TestFetchChecksRedirectDestinationBeforeConnecting(t *testing.T) {
	var hits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer private.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/?token=PRIVATE_TOKEN", http.StatusFound)
	}))
	defer redirect.Close()
	allowedAddress := strings.TrimPrefix(redirect.URL, "http://")
	policy := func(address string) error {
		if address == allowedAddress {
			return nil
		}
		return checkFetchAddress(address)
	}
	body, err := fetchListWithPolicy(context.Background(), redirect.URL, "", policy)
	if !errors.Is(err, errForbiddenFetchAddress) || body != "" || hits.Load() != 0 {
		t.Fatalf("redirect reached protected service: body=%q err=%v hits=%d", body, err, hits.Load())
	}
	if strings.Contains(err.Error(), "PRIVATE_TOKEN") {
		t.Fatal("redirect token exposed")
	}
}

func TestFetchPolicyFailureDoesNotUseSystemFallback(t *testing.T) {
	calls := 0
	_, err := fetchWithFallback(context.Background(), "wg0", func(context.Context, string) (string, error) {
		calls++
		return "", &fetchError{errForbiddenFetchAddress.Error(), errForbiddenFetchAddress}
	})
	if calls != 1 || !errors.Is(err, errForbiddenFetchAddress) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestFetchErrorsHideURLAndRedirectTokens(t *testing.T) {
	for _, mode := range []string{"closed", "invalid redirect", "partial body"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "invalid redirect" {
					w.Header().Set("Location", "http://example.com/%ZZ_BAD_REDIRECT_TOKEN")
					w.WriteHeader(http.StatusFound)
				} else {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("BODY_SECRET"))
				}
			}))
			defer server.Close()
			if mode == "closed" {
				server.Close()
			}
			body, err := fetchTestList(server.URL + "/PATH_TOKEN?token=QUERY_TOKEN")
			if err == nil || body != "" {
				t.Fatalf("body=%q err=%v", body, err)
			}
			for _, secret := range []string{server.URL, "PATH_TOKEN", "QUERY_TOKEN", "BAD_REDIRECT_TOKEN", "BODY_SECRET"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposes %s: %v", secret, err)
				}
			}
		})
	}
}
