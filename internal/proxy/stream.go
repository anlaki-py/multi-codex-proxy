package proxy

import (
	"encoding/json"
	"time"
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
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		idx := t.nextIndex
		t.nextIndex++
		if callID != "" {
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
