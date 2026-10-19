package api

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"magitrickle/api/utils"
	"magitrickle/app"
	"magitrickle/models"
)

func hostProtection(a app.Main) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowedHTTPHost(r, a.Config().HTTPWeb) {
				utils.WriteError(w, http.StatusForbidden, "HTTP Host is not allowed; configure app.httpWeb.allowedHosts for a custom hostname")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allowedHTTPHost(r *http.Request, config models.AppConfigHTTPWeb) bool {
	u, err := url.Parse("http://" + r.Host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	host, err := models.NormalizeHTTPHost(u.Hostname())
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	for _, configured := range append([]string{config.Host.Address}, config.AllowedHosts...) {
		if normalized, err := models.NormalizeHTTPHost(configured); err == nil && normalized == host && host != "0.0.0.0" && host != "::" {
			return true
		}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.IsUnspecified() {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && local.IP.Equal(net.IP(ip.AsSlice())) {
		return true
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if prefix, ok := address.(*net.IPNet); ok && prefix.IP.Equal(net.IP(ip.AsSlice())) {
			return true
		}
	}
	return false
}
