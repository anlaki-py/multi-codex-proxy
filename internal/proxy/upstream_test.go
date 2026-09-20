package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"multi-codex-proxy/internal/accounts"
)

func fakeJWT() string {
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	payload := map[string]any{
		"sub": "u1", "email": "t@example.com", "name": "T",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-1", "chatgpt_user_id": "u1",
		},
	}
	return enc(map[string]any{"alg": "none"}) + "." + enc(payload) + ".sig"
}

func testServerWithAccount(t *testing.T, client *http.Client) *Server {
	t.Helper()
	repo, err := accounts.NewRepository(accounts.NewStore(t.TempDir()), client)
	if err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf(`{"id_token":%q,"access_token":"a","refresh_token":"r","expires_in":3600}`, fakeJWT())
	if _, err := repo.SaveLogin(token); err != nil {
		t.Fatal(err)
	}
	return &Server{repo: repo, client: client, ua: "test", baseURL: "http://unused", log: func(string, ...any) {}}
}

func TestUnsupportedParamStrippedAndRetried(t *testing.T) {
	var seen []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		seen = append(seen, b)
		if _, ok := b["max_output_tokens"]; ok {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": "Unsupported parameter: max_output_tokens"})
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"resp-1"}`))
	}))
	defer up.Close()

	s := testServerWithAccount(t, up.Client())
	s.baseURL = up.URL
	resp, _, err := s.postUpstream(context.Background(),
		map[string]any{"model": "m", "max_output_tokens": 50, "input": "hi"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if len(seen) != 2 {
		t.Fatalf("want 2 upstream calls, got %d", len(seen))
	}
	if _, ok := seen[1]["max_output_tokens"]; ok {
		t.Fatal("param not stripped on retry")
	}
}

func TestOther400PassesThroughReadable(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer up.Close()

	s := testServerWithAccount(t, up.Client())
	s.baseURL = up.URL
	resp, _, err := s.postUpstream(context.Background(), map[string]any{"model": "m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || strings.TrimSpace(string(raw)) != `{"error":"nope"}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestFindUnsupportedParam(t *testing.T) {
	cases := []struct {
		body string
		want string
		ok   bool
	}{
		{`{"detail":"Unsupported parameter: max_output_tokens"}`, "max_output_tokens", true},
		{`{"error":{"message":"bad request: Unsupported parameter: temperature"}}`, "temperature", true},
		{`{"error":"nope"}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		got, ok := findUnsupportedParam([]byte(c.body))
		if got != c.want || ok != c.ok {
			t.Fatalf("body %s: got %q,%v", c.body, got, ok)
		}
	}
}
