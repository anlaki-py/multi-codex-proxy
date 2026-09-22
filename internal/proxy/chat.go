package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// chatCompletions speaks Chat Completions to clients, Responses upstream.
// Non-stream converts the full object. Stream translates SSE event by event.
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method must be POST", "POST a JSON body with model and messages")
		return
	}
	if !s.checkAuth(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error(), "keep requests under 10 MiB")
		return
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		writeErr(w, 400, "body must be a JSON object", "example: {\"model\":\"gpt-5-codex\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
		return
	}
	model, _ := in["model"].(string)
	if model == "" {
		writeErr(w, 400, "body.model is required", "list valid ids via GET /v1/models")
		return
	}
	stream, _ := in["stream"].(bool)
	respBody, err := chatToResponses(in)
	if err != nil {
		writeAppErr(w, statusForErr(err), err)
		return
	}
	if !stream {
		s.chatNonStream(w, r, model, respBody)
		return
	}
	s.chatStream(w, r, model, respBody)
}

// chatNonStream streams upstream (the only mode Codex serves) and folds
// the collected result into one chat completion.
func (s *Server) chatNonStream(w http.ResponseWriter, r *http.Request, model string, respBody map[string]any) {
	respBody["stream"] = true
	resp, acct, err := s.postUpstream(r.Context(), respBody, true)
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
	up, err := collectResponses(resp.Body, model)
	if err != nil {
		writeAppErr(w, 502, err)
		return
	}
	writeJSON(w, 200, responsesToChat(up, model))
}

// chatStream POSTs upstream with stream true and translates each SSE event
// into chat.completion.chunk frames. Closes with data: [DONE].
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request, model string, respBody map[string]any) {
	resp, acct, err := s.postUpstream(r.Context(), respBody, true)
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	tr := newChatStreamTranslator(model)
	closed := false
	emit := func(payload string) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	finish := func() {
		if !closed {
			closed = true
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	forEachSSEFrame(resp.Body, func(raw string) bool {
		if raw == "[DONE]" {
			finish()
			return false
		}
		lines, done := tr.translate(raw)
		for _, l := range lines {
			emit(l)
		}
		if done {
			finish()
			return false
		}
		return !closed
	})
	if !tr.finished && !closed {
		raw, _ := json.Marshal(map[string]any{"error": "upstream stream cut off"})
		emit(string(raw))
	}
	finish()
}
