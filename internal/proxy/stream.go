package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"multi-codex-proxy/internal/apperr"
)

// chatStreamTranslator converts one Responses SSE stream into chat chunks.
// call_id values pass through as chat tool ids so harnesses can answer.
type chatStreamTranslator struct {
	model     string
	id        string
	created   int64
	toolIndex map[string]int
	nextIndex int
	hasTools  bool
	finished  bool
}

func newChatStreamTranslator(model string) *chatStreamTranslator {
	return &chatStreamTranslator{
		model: model, id: "chatcmpl-fallback",
		created: time.Now().Unix(), toolIndex: map[string]int{},
	}
}

func (t *chatStreamTranslator) chunk(delta map[string]any, finish *string) string {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	raw, _ := json.Marshal(map[string]any{
		"id": t.id, "object": "chat.completion.chunk",
		"created": t.created, "model": t.model,
		"choices": []any{choice},
	})
	return string(raw)
}

// translate maps one Responses SSE data payload to chat chunk lines.
// done true means the caller must close with data: [DONE].
func (t *chatStreamTranslator) translate(raw string) (lines []string, done bool) {
	var ev map[string]any
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return nil, false
	}
	typ, _ := ev["type"].(string)
	switch typ {
	case "response.created":
		if r, ok := ev["response"].(map[string]any); ok {
			if id, _ := r["id"].(string); id != "" {
				t.id = id
			}
		}
		return nil, false
	case "response.output_text.delta":
		d, _ := ev["delta"].(string)
		if d == "" {
			return nil, false
		}
		return []string{t.chunk(map[string]any{"content": d}, nil)}, false
	case "response.output_item.added":
		item, _ := ev["item"].(map[string]any)
		if item == nil || item["type"] != "function_call" {
			return nil, false
		}
		// item_id in later deltas matches item.id, not call_id,
		// so register under both keys.
		itemID, _ := item["id"].(string)
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		key := itemID
		if key == "" {
			key = callID
		}
		idx := t.nextIndex
		t.nextIndex++
		t.toolIndex[key] = idx
		if callID != "" && callID != key {
			t.toolIndex[callID] = idx
		}
		t.hasTools = true
		return []string{t.chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": idx, "id": callID, "type": "function",
			"function": map[string]any{"name": name, "arguments": ""},
		}}}, nil)}, false
	case "response.function_call_arguments.delta":
		itemID, _ := ev["item_id"].(string)
		d, _ := ev["delta"].(string)
		if d == "" {
			return nil, false
		}
		idx, ok := t.toolIndex[itemID]
		if !ok {
			idx = t.nextIndex
			t.nextIndex++
			t.toolIndex[itemID] = idx
			t.hasTools = true
		}
		return []string{t.chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": idx, "function": map[string]any{"arguments": d},
		}}}, nil)}, false
	case "response.output_text.done", "response.function_call_arguments.done",
		"response.output_item.done", "response.content_part.done":
		return nil, false
	case "response.completed":
		finish := "stop"
		if t.hasTools {
			finish = "tool_calls"
		}
		if r, ok := ev["response"].(map[string]any); ok {
			if status, _ := r["status"].(string); status == "incomplete" {
				finish = "length"
			}
		}
		t.finished = true
		return []string{t.chunk(map[string]any{}, &finish)}, true
	case "response.failed":
		msg := "upstream response failed"
		if r, ok := ev["response"].(map[string]any); ok {
			if e, ok := r["error"].(map[string]any); ok {
				if m, _ := e["message"].(string); m != "" {
					msg = m
				}
			}
		}
		raw, _ := json.Marshal(map[string]any{"error": msg})
		return []string{string(raw)}, true
	case "response.incomplete":
		finish := "length"
		t.finished = true
		return []string{t.chunk(map[string]any{}, &finish)}, true
	case "error":
		msg := "upstream error"
		if m, _ := ev["message"].(string); m != "" {
			msg = m
		}
		raw, _ := json.Marshal(map[string]any{"error": msg})
		return []string{string(raw)}, true
	default:
		return nil, false
	}
}

// forEachSSEFrame calls fn with each SSE data payload in order.
// fn returns false to stop reading early.
func forEachSSEFrame(r io.Reader, fn func(raw string) bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var data strings.Builder
	flush := func() bool {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" {
			return true
		}
		return fn(raw)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
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
	flush()
}

type collectedCall struct {
	callID string
	name   string
	args   strings.Builder
}

// collectResponses rebuilds a Responses object from an upstream SSE stream.
// Used for non-stream clients: upstream only speaks stream:true.
func collectResponses(r io.Reader, model string) (map[string]any, error) {
	id := ""
	status := "completed"
	var incompleteReason string
	var text strings.Builder
	calls := map[string]*collectedCall{}
	order := []string{}
	usage := map[string]any{}
	failed := ""
	remember := func(key, callID, name string) *collectedCall {
		if c, ok := calls[key]; ok {
			return c
		}
		c := &collectedCall{callID: callID, name: name}
		calls[key] = c
		order = append(order, key)
		return c
	}
	forEachSSEFrame(r, func(raw string) bool {
		if raw == "[DONE]" {
			return false
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return true
		}
		switch ev["type"] {
		case "response.created":
			if resp, ok := ev["response"].(map[string]any); ok {
				id, _ = resp["id"].(string)
			}
		case "response.output_text.delta":
			d, _ := ev["delta"].(string)
			text.WriteString(d)
		case "response.output_item.added":
			item, _ := ev["item"].(map[string]any)
			if item == nil || item["type"] != "function_call" {
				return true
			}
			itemID, _ := item["id"].(string)
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			key := itemID
			if key == "" {
				key = callID
			}
			c := remember(key, callID, name)
			if itemID != "" && itemID != key {
				calls[itemID] = c
			}
			if callID != "" && callID != key {
				calls[callID] = c
			}
		case "response.function_call_arguments.delta":
			itemID, _ := ev["item_id"].(string)
			d, _ := ev["delta"].(string)
			c, ok := calls[itemID]
			if !ok {
				c = remember(itemID, itemID, "")
			}
			c.args.WriteString(d)
		case "response.completed":
			if resp, ok := ev["response"].(map[string]any); ok {
				status, _ = resp["status"].(string)
				if status == "" {
					status = "completed"
				}
				if u, ok := resp["usage"].(map[string]any); ok {
					usage = u
				}
				if det, ok := resp["incomplete_details"].(map[string]any); ok {
					incompleteReason, _ = det["reason"].(string)
				}
			}
			return false
		case "response.incomplete":
			status = "incomplete"
			return false
		case "response.failed":
			status = "failed"
			failed = "upstream response failed"
			if resp, ok := ev["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if m, _ := e["message"].(string); m != "" {
						failed = m
					}
				}
			}
			return false
		case "error":
			failed = "upstream error"
			if m, _ := ev["message"].(string); m != "" {
				failed = m
			}
			return false
		}
		return true
	})
	if failed != "" {
		return nil, apperr.New("upstream stream", apperr.CodeUpstream,
			fmt.Errorf("%s", failed),
			"retry the request")
	}
	output := []any{}
	if text.Len() > 0 {
		output = append(output, map[string]any{"type": "message",
			"content": []any{map[string]any{"type": "output_text", "text": text.String()}}})
	}
	seen := map[*collectedCall]bool{}
	for _, key := range order {
		c := calls[key]
		if seen[c] {
			continue
		}
		seen[c] = true
		args := c.args.String()
		if args == "" {
			args = "{}"
		}
		output = append(output, map[string]any{"type": "function_call",
			"call_id": c.callID, "name": c.name, "arguments": args})
	}
	if id == "" {
		id = "resp-collected"
	}
	obj := map[string]any{
		"id": id, "model": model, "status": status,
		"output": output, "usage": usage,
	}
	if incompleteReason != "" {
		obj["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	return obj, nil
}
