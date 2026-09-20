package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// chatCompletions speaks Chat Completions to clients, Responses upstream.
// Non-stream converts the full object. Stream translates SSE event by event.
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method must be POST", "POST a JSON body with model and messages")
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

// chatNonStream reuses the responses handler, then folds output to chat shape.
func (s *Server) chatNonStream(w http.ResponseWriter, r *http.Request, model string, respBody map[string]any) {
	fwd, _ := json.Marshal(respBody)
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "/v1/responses", strings.NewReader(string(fwd)))
	req.Header.Set("Content-Type", "application/json")
	rec := &captureWriter{header: http.Header{}}
	s.responses(rec, req)
	if rec.code == 0 {
		rec.code = 200
	}
	if rec.code < 200 || rec.code >= 300 {
		w.WriteHeader(rec.code)
		_, _ = w.Write(rec.buf)
		return
	}
	var up map[string]any
	if err := json.Unmarshal(rec.buf, &up); err != nil {
		writeErr(w, 502, "upstream: bad JSON", "retry; if it repeats, upstream changed shape")
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
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var data strings.Builder
	flushFrame := func() {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" {
			return
		}
		if raw == "[DONE]" {
			finish()
			return
		}
		lines, done := tr.translate(raw)
		for _, l := range lines {
			emit(l)
		}
		if done {
			finish()
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flushFrame()
			if closed {
				return
			}
			continue
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimSpace(payload))
		}
	}
	flushFrame()
	if !tr.finished && !closed {
		raw, _ := json.Marshal(map[string]any{"error": "upstream stream cut off"})
		emit(string(raw))
	}
	finish()
}

type captureWriter struct {
	header http.Header
	buf    []byte
	code   int
}

func (c *captureWriter) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.buf = append(c.buf, b...)
	return len(b), nil
}

func (c *captureWriter) WriteHeader(code int) { c.code = code }
