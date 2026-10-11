package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Use a separate disposable database: these tests must never reset a real
// instance's authentication setting, even when other tests use that database.
func isolatedAuthServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN required")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := "artex_auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminURL := *u
	adminURL.Path = "/postgres"
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err = admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE "` + dbName + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + dbName
	t.Setenv("ARTEX_PG_DSN", u.String())
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return &Server{m: m, jwtKey: []byte("01234567890123456789012345678901")}
}

func TestAuthHardeningLifecycle(t *testing.T) {
	s := isolatedAuthServer(t)
	for _, password := range []string{"x", strings.Repeat("x", 73)} {
		body, _ := json.Marshal(map[string]string{"password": password})
		w := httptest.NewRecorder()
		s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", strings.NewReader(string(body))))
		if w.Code != 400 {
			t.Fatalf("invalid password: %d %s", w.Code, w.Body)
		}
	}
	const n = 8
	results := make(chan struct {
		code            int
		password, token string
	}, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			password := fmt.Sprintf("correct-password-%d", i)
			body, _ := json.Marshal(map[string]string{"password": password})
			w := httptest.NewRecorder()
			s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", strings.NewReader(string(body))))
			var response struct{ Token string }
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			results <- struct {
				code            int
				password, token string
			}{w.Code, password, response.Token}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	winner, token := "", ""
	for result := range results {
		if result.code == 200 {
			successes++
			winner = result.password
			token = result.token
		} else if result.code != 403 {
			t.Fatalf("unexpected init status %d", result.code)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent setup issued %d successes", successes)
	}
	hash, ok, err := s.m.pg.GetSetting(authPassKey)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(winner)) != nil {
		t.Fatal("winning password overwritten")
	}
	if !verifyJWT(token, s.jwtKey) {
		t.Fatal("setup token invalid")
	}
	for _, tc := range []struct {
		password string
		want     int
	}{{"wrong", 401}, {winner, 200}} {
		body, _ := json.Marshal(map[string]string{"username": "ARTEX", "password": tc.password})
		w := httptest.NewRecorder()
		s.authLogin(w, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body))))
		if w.Code != tc.want {
			t.Fatalf("login: %d want %d", w.Code, tc.want)
		}
	}
	for _, tc := range []struct {
		password string
		want     int
	}{{"x", 400}, {"replacement-password", 200}} {
		body, _ := json.Marshal(map[string]string{"old_password": winner, "new_password": tc.password})
		r := httptest.NewRequest("POST", "/api/auth/change-password", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.authChangePassword(w, r)
		if w.Code != tc.want {
			t.Fatalf("change password: %d %s", w.Code, w.Body)
		}
	}
	if err := s.m.pg.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.authLogin(w, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"ARTEX","password":"replacement-password"}`)))
	if w.Code != 503 {
		t.Fatalf("login DB failure: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/auth/change-password", strings.NewReader(`{"old_password":"replacement-password","new_password":"another-password"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.authChangePassword(w, r)
	if w.Code != 503 {
		t.Fatalf("change DB failure: %d", w.Code)
	}
}

func TestVerifyJWTRequiresExpectedClaimsAndAlgorithm(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	valid, err := signJWT(key)
	if err != nil || !verifyJWT(valid, key) {
		t.Fatal("valid token rejected")
	}
	for _, tc := range []struct {
		method jwt.SigningMethod
		claims jwt.RegisteredClaims
	}{
		{jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "ARTEX"}},
		{jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "other", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}},
		{jwt.SigningMethodHS512, jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}},
		{jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}},
	} {
		token, err := jwt.NewWithClaims(tc.method, tc.claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		if verifyJWT(token, key) {
			t.Fatal("invalid token accepted")
		}
	}
}
