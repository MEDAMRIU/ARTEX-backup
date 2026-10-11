package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginBoundary(t *testing.T) {
	t.Setenv("ARTEX_ALLOWED_ORIGINS", "https://trusted.example")
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"", 200}, {"http://localhost:8787", 200}, {"http://localhost:3000", 200},
		{"https://trusted.example", 200}, {"https://attacker.example", 403}, {"null", 403},
		{"http://localhost:8787@attacker.example", 403}, {"http://localhost:8787/evil", 403},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost:8787/api/auth/init", nil)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if w.Header().Get("Access-Control-Allow-Origin") == "*" {
				t.Fatal("wildcard origin")
			}
		})
	}
}
