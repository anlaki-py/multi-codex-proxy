package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// chatCompletions is a small compat shim, not a full port.
// It maps OpenAI chat messages to a Responses call, then folds
// the text output back into chat format. Tools and images pass
// through only when the model already accepts them upstream.
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
	msgs, _ := in["messages"].([]any)
	if len(msgs) == 0 {
		writeErr(w, 400, "body.messages must not be empty", "add at least one user message")
		return
	}
	stream, _ := in["stream"].(bool)

	respBody := map[string]any{
		"model":        model,
		"stream":       stream,
		"store":        false,
		"input":        toResponsesInput(msgs),
		"instructions": systemText(msgs),
	}
	if v, ok := in["max_tokens"].(float64); ok && v > 0 {
		respBody["max_output_tokens"] = int(v)
	}
	if v, ok := in["max_completion_tokens"].(float64); ok && v > 0 {
		respBody["max_output_tokens"] = int(v)
	}
	if v, ok := in["temperature"].(float64); ok {
		respBody["temperature"] = v
	}
	if v, ok := in["top_p"].(float64); ok {
		respBody["top_p"] = v
	}

	// Reuse the responses path by issuing an internal request.
	fwd, _ := json.Marshal(respBody)
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "/v1/responses", strings.NewReader(string(fwd)))
	req.Header.Set("Content-Type", "application/json")
	// Call handler directly so rotation, headers, usage stay in one place.
	if stream {
		// For streams, clients expect chat SSE. We proxy responses SSE
		// but relabel is out of scope for v1, so return the native stream
		// with a clear marker instead of corrupt data.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"error\": \"chat stream uses /v1/responses stream for now\"}\n\n")
		return
	}
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
	text := responsesText(up)
	usage := map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	if u, _ := up["usage"].(map[string]any); u != nil {
		usage["prompt_tokens"] = u["input_tokens"]
		usage["completion_tokens"] = u["output_tokens"]
		usage["total_tokens"] = u["total_tokens"]
	}
	writeJSON(w, 200, map[string]any{
		"id":      up["id"],
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant", "content": text,
			},
			"finish_reason": "stop",
		}},
		"usage": usage,
	})
}

func toResponsesInput(msgs []any) []any {
	out := []any{}
	for _, m := range msgs {
		obj, _ := m.(map[string]any)
		if obj == nil {
			continue
		}
		role, _ := obj["role"].(string)
		if role == "system" {
			continue
		}
		content := plainText(obj["content"])
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out
}

func systemText(msgs []any) string {
	parts := []string{}
	for _, m := range msgs {
		obj, _ := m.(map[string]any)
		if obj == nil || obj["role"] != "system" {
			continue
		}
		if t := plainText(obj["content"]); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "You are a helpful assistant."
	}
	return strings.Join(parts, "\n")
}

func plainText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := []string{}
		for _, p := range t {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if s, _ := pm["text"].(string); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func responsesText(up map[string]any) string {
	items, _ := up["output"].([]any)
	parts := []string{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil || m["type"] != "message" {
			continue
		}
		content, _ := m["content"].([]any)
		for _, c := range content {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			if cm["type"] == "output_text" {
				if t, _ := cm["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
	}
	return strings.Join(parts, "")
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
