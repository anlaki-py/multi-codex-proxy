package proxy

import (
	"net/http/httptest"
	"testing"

	"multi-codex-proxy/internal/accounts"
)

func testAuthServer(t *testing.T, apiKey string) *Server {
	t.Helper()
	repo, err := accounts.NewRepository(accounts.NewStore(t.TempDir()), NewClient())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{repo: repo, client: NewClient(), ua: "test", baseURL: "http://unused", APIKey: apiKey, log: func(string, ...any) {}}
}

func TestOpenAcceptsAnyKey(t *testing.T) {
	s := testAuthServer(t, "")
	for _, key := range []string{"", "anything", "sk-random"} {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code == 401 {
			t.Fatalf("open server rejected key %q", key)
		}
	}
}

func TestLockedRequiresExactKey(t *testing.T) {
	s := testAuthServer(t, "secret123")
	cases := []struct {
		name string
		key  string
		want int
	}{
		{"missing", "", 401},
		{"wrong", "nope", 401},
		{"right", "secret123", 503},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestHealthSkipsAuth(t *testing.T) {
	s := testAuthServer(t, "secret123")
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("health: got %d want 200", rec.Code)
	}
}

func TestLockedChatAndResponsesNeedKey(t *testing.T) {
	s := testAuthServer(t, "secret123")
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		req := httptest.NewRequest("POST", path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%s without key: got %d want 401", path, rec.Code)
		}
	}
}
