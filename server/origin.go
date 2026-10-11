package server

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Allow the embedded UI, the existing local Next.js development UI, and exact
// operator-configured origins. An unrelated website must not initialize or
// mutate a local ARTEX instance through the visitor's browser.
func allowedOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	loopback := func(h string) bool { return h == "localhost" || (net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()) }
	if loopback(host) && loopback(u.Hostname()) && u.Port() == "3000" {
		return true
	}
	for _, trusted := range strings.Split(os.Getenv("ARTEX_ALLOWED_ORIGINS"), ",") {
		if origin == strings.TrimSpace(trusted) {
			return true
		}
	}
	return false
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		if (origin != "" && !allowedOrigin(r, origin)) || (origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			writeErr(w, http.StatusForbidden, "请求来源不被允许")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
