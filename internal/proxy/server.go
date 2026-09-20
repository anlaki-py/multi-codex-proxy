package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	"multi-codex-proxy/internal/accounts"
	"multi-codex-proxy/internal/codex"
)

// Server serves an OpenAI compatible surface backed by rotating Codex accounts.
// Routes: GET /health, GET /v1/models, POST /v1/responses, POST /v1/chat/completions.
type Server struct {
	repo   *accounts.Repository
	client *http.Client
	ua     string
	log    func(format string, args ...any)
}

func NewClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
		Timeout: 10 * time.Minute,
	}
}

func NewServer(repo *accounts.Repository, client *http.Client, log func(string, ...any)) *Server {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Server{repo: repo, client: client, ua: codex.UserAgent(runtime.GOOS, runtime.GOARCH), log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/responses", s.responses)
	mux.HandleFunc("/v1/chat/completions", s.chatCompletions)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "upstream": codex.CodexAPI})
}

// models mirrors CodexProvider.listModels: proxy the upstream list,
// keep only visibility == list, return OpenAI list shape.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method must be GET")
		return
	}
	acct, err := s.repo.Acquire()
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET",
		codex.CodexAPI+"/models?client_version="+codex.ClientVersion, nil)
	s.setUpstreamHeaders(req, acct)
	resp, err := s.client.Do(req)
	if err != nil {
		writeErr(w, 502, "upstream models: "+err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 401 {
		s.repo.MarkInvalid(acct.ID)
		writeErr(w, 502, "upstream models: HTTP 401, account marked invalid")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeErr(w, 502, fmt.Sprintf("upstream models: HTTP %d", resp.StatusCode))
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeErr(w, 502, "upstream models: bad JSON")
		return
	}
	items, _ := parsed["models"].([]any)
	data := []any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if m["visibility"] != "list" {
			continue
		}
		slug, _ := m["slug"].(string)
		if slug == "" {
			continue
		}
		data = append(data, map[string]any{
			"id": slug, "object": "model", "owned_by": "codex",
		})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// responses is a straight passthrough to POST CodexAPI/responses.
// It injects default instructions like CodexProvider.withDefaultInstructions
// and streams SSE back verbatim when stream is true.
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method must be POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		writeErr(w, 400, "body must be JSON object")
		return
	}
	if _, ok := body["model"].(string); !ok {
		writeErr(w, 400, "body.model is required")
		return
	}
	stream, _ := body["stream"].(bool)
	ensureInstructions(body)

	acct, err := s.repo.Acquire()
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	upBody, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(r.Context(), "POST", codex.CodexAPI+"/responses", bytes.NewReader(upBody))
	s.setUpstreamHeaders(req, acct)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	s.log("responses model=%v account=%s stream=%v", body["model"], acct.Email, stream)

	resp, err := s.client.Do(req)
	if err != nil {
		writeErr(w, 502, "upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()
	s.afterUpstream(acct.ID, resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		upErr, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(upErr)
		return
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 20<<20))
		return
	}
	// SSE passthrough with flush, verbatim bytes.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}

func (s *Server) setUpstreamHeaders(req *http.Request, acct codex.Account) {
	req.Header.Set("Authorization", "Bearer "+acct.AccessToken)
	req.Header.Set("ChatGPT-Account-Id", acct.ChatGPTAccount)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", codex.Originator)
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
}

// afterUpstream mirrors responseApiFor interceptor: save quota headers,
// retire account on 401. Never logs tokens.
func (s *Server) afterUpstream(accountID string, resp *http.Response) {
	if resp.StatusCode == 401 {
		s.repo.MarkInvalid(accountID)
		return
	}
	if usage := codex.ParseUsageHeaders(resp.Header); usage != nil {
		s.repo.UpdateUsage(accountID, usage)
	}
}

// ensureInstructions mirrors withDefaultInstructions in CodexProvider.kt:155.
func ensureInstructions(body map[string]any) {
	if v, _ := body["instructions"].(string); v != "" {
		return
	}
	body["instructions"] = "You are a helpful assistant."
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "code": code}})
}
