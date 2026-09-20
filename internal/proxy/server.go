package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"time"

	"multi-codex-proxy/internal/accounts"
	"multi-codex-proxy/internal/apperr"
	"multi-codex-proxy/internal/codex"
)

// Server serves an OpenAI compatible surface backed by rotating Codex accounts.
// Routes: GET /health, GET /v1/models, POST /v1/responses, POST /v1/chat/completions.
type Server struct {
	repo    *accounts.Repository
	client  *http.Client
	ua      string
	baseURL string
	log     func(format string, args ...any)
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
	return &Server{repo: repo, client: client, ua: codex.UserAgent(runtime.GOOS, runtime.GOARCH), baseURL: codex.CodexAPI, log: log}
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
		writeErr(w, 405, "method must be GET", "use GET /v1/models with no body")
		return
	}
	acct, err := s.repo.Acquire()
	if err != nil {
		writeAppErr(w, 503, err)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET",
		s.baseURL+"/models?client_version="+codex.ClientVersion, nil)
	s.setUpstreamHeaders(req, acct)
	resp, err := s.client.Do(req)
	if err != nil {
		writeErr(w, 502, "upstream models unreachable: "+err.Error(), "check network, retry in a minute")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 401 {
		s.repo.MarkInvalid(acct.ID)
		writeErr(w, 502, "upstream models: HTTP 401, account marked invalid", "press a in TUI to sign this account in again")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeErr(w, 502, fmt.Sprintf("upstream models: HTTP %d", resp.StatusCode), "transient upstream error, retry shortly")
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeErr(w, 502, "upstream models: bad JSON", "retry; if it repeats, upstream changed shape")
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

// responses proxies POST CodexAPI/responses.
// It injects default instructions like CodexProvider.withDefaultInstructions.
// Upstream only speaks stream:true, so non-stream requests are collected
// server side and returned as one object. Stream requests pass SSE through.
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method must be POST", "POST a JSON body with model and input")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error(), "keep requests under 10 MiB")
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		writeErr(w, 400, "body must be a JSON object", "example: {\"model\":\"gpt-5-codex\",\"input\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		writeErr(w, 400, "body.model is required", "list valid ids via GET /v1/models")
		return
	}
	if len(raw) == 0 {
		writeErr(w, 400, "empty body", "send a JSON object with model and input")
		return
	}
	stream, _ := body["stream"].(bool)
	ensureInstructions(body)
	// Codex only serves stream:true. Non-stream clients get a collected
	// object below, so always ask upstream for SSE.
	body["stream"] = true

	resp, acct, err := s.postUpstream(r.Context(), body, true)
	if err != nil {
		writeAppErr(w, statusForErr(err), err)
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
		up, err := collectResponses(resp.Body, model)
		if err != nil {
			writeAppErr(w, 502, err)
			return
		}
		writeJSON(w, 200, up)
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

// postUpstream acquires a healthy account and POSTs a Responses body.
// Params the upstream rejects as unsupported are dropped with one retry
// each instead of failing the client request. Caller closes resp.Body
// and feeds resp to afterUpstream.
func (s *Server) postUpstream(ctx context.Context, body map[string]any, stream bool) (*http.Response, codex.Account, error) {
	acct, err := s.repo.Acquire()
	if err != nil {
		return nil, codex.Account{}, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		resp, err := s.postOnce(ctx, body, stream, acct)
		if err != nil {
			return nil, codex.Account{}, err
		}
		if resp.StatusCode != 400 {
			return resp, acct, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		param, ok := findUnsupportedParam(raw)
		if !ok {
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			return resp, acct, nil
		}
		delete(body, param)
		s.log("upstream rejects param %s, dropping and retrying", param)
	}
	return nil, codex.Account{}, apperr.New("upstream call", apperr.CodeUpstream,
		fmt.Errorf("upstream keeps rejecting params after 3 drops"),
		"narrow the request fields to model, input, instructions")
}

func (s *Server) postOnce(ctx context.Context, body map[string]any, stream bool, acct codex.Account) (*http.Response, error) {
	upBody, err := json.Marshal(body)
	if err != nil {
		return nil, apperr.New("upstream encode", apperr.CodeInput, err,
			"request could not be serialized")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+"/responses", bytes.NewReader(upBody))
	if err != nil {
		return nil, apperr.New("upstream request", apperr.CodeUpstream, err,
			"retry the request")
	}
	s.setUpstreamHeaders(req, acct)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	s.log("responses model=%v account=%s stream=%v", body["model"], acct.Email, stream)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, apperr.New("upstream call", apperr.CodeUpstream, err,
			"check network; request was not charged, safe to retry")
	}
	return resp, nil
}

// unsupportedParamRe matches "Unsupported parameter: <name>" in upstream
// 400 bodies, whether under detail or error.message.
var unsupportedParamRe = regexp.MustCompile(`Unsupported parameter:\s*"?([A-Za-z0-9_.\-]+)"?`)

func findUnsupportedParam(raw []byte) (string, bool) {
	m := unsupportedParamRe.FindSubmatch(raw)
	if len(m) != 2 || len(m[1]) == 0 {
		return "", false
	}
	return string(m[1]), true
}

func statusForErr(err error) int {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperr.CodeAccounts, apperr.CodeAuth:
			return 503
		case apperr.CodeInput:
			return 400
		}
	}
	return 502
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

func writeErr(w http.ResponseWriter, code int, msg, hint string) {
	errObj := map[string]any{"message": msg, "code": code}
	if hint != "" {
		errObj["hint"] = hint
	}
	writeJSON(w, code, map[string]any{"error": errObj})
}

// writeAppErr unwraps apperr.Error so API clients see message plus hint.
func writeAppErr(w http.ResponseWriter, code int, err error) {
	msg, hint := apperr.UserMessage(err)
	if msg == "" {
		msg = "unknown error"
	}
	writeErr(w, code, msg, hint)
}
