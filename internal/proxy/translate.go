package proxy

import (
	"fmt"
	"strings"
	"time"

	"multi-codex-proxy/internal/apperr"
)

// Translation between OpenAI Chat Completions and Responses.
// All funcs here are pure: no network, no repo. Easy to unit test.

// chatToResponses maps a chat request body to a Responses body.
// System messages fold into instructions, assistant tool_calls become
// function_call items, tool messages become function_call_output items.
func chatToResponses(in map[string]any) (map[string]any, error) {
	model, _ := in["model"].(string)
	if model == "" {
		return nil, apperr.New("translate", apperr.CodeInput,
			fmt.Errorf("body.model is required"),
			"list valid ids via GET /v1/models")
	}
	msgs, _ := in["messages"].([]any)
	if len(msgs) == 0 {
		return nil, apperr.New("translate", apperr.CodeInput,
			fmt.Errorf("body.messages must not be empty"),
			"add at least one user message")
	}
	stream, _ := in["stream"].(bool)
	out := map[string]any{
		"model":        model,
		"stream":       stream,
		"store":        false,
		"input":        chatMessagesToInput(msgs),
		"instructions": systemText(msgs),
	}
	if v, ok := num(in["max_tokens"]); ok {
		out["max_output_tokens"] = v
	}
	if v, ok := num(in["max_completion_tokens"]); ok {
		out["max_output_tokens"] = v
	}
	if v, ok := num(in["temperature"]); ok {
		out["temperature"] = v
	}
	if v, ok := num(in["top_p"]); ok {
		out["top_p"] = v
	}
	if v, ok := in["reasoning_effort"].(string); ok && v != "" {
		out["reasoning"] = map[string]any{"effort": v, "summary": "auto"}
	}
	if tools := chatToolsToResponses(in["tools"]); tools != nil {
		out["tools"] = tools
	}
	if choice := chatChoiceToResponses(in["tool_choice"]); choice != nil {
		out["tool_choice"] = choice
	}
	return out, nil
}

func chatMessagesToInput(msgs []any) []any {
	out := []any{}
	for _, m := range msgs {
		obj, _ := m.(map[string]any)
		if obj == nil {
			continue
		}
		role, _ := obj["role"].(string)
		switch role {
		case "system":
			continue
		case "tool":
			callID, _ := obj["tool_call_id"].(string)
			if callID == "" {
				callID, _ = obj["id"].(string)
			}
			out = append(out, map[string]any{
				"type": "function_call_output", "call_id": callID,
				"output": plainText(obj["content"]),
			})
		case "assistant":
			text := chatContentText(obj["content"])
			calls, _ := obj["tool_calls"].([]any)
			if text != "" {
				out = append(out, map[string]any{"role": "assistant", "content": text})
			}
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				fn, _ := cm["function"].(map[string]any)
				name, _ := fn["name"].(string)
				args, _ := fn["arguments"].(string)
				if args == "" {
					args = "{}"
				}
				id, _ := cm["id"].(string)
				out = append(out, map[string]any{
					"type": "function_call", "call_id": id,
					"name": name, "arguments": args,
				})
			}
			if text == "" && len(calls) == 0 {
				continue
			}
		default:
			out = append(out, map[string]any{"role": "user", "content": chatContentParts(obj["content"])})
		}
	}
	return out
}

// chatContentParts keeps text and image parts in Responses input shape.
func chatContentParts(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	out := []any{}
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		kind, _ := pm["type"].(string)
		switch kind {
		case "image_url":
			iu := pm["image_url"]
			url := ""
			if m, ok := iu.(map[string]any); ok {
				url, _ = m["url"].(string)
			} else if s, ok := iu.(string); ok {
				url = s
			}
			out = append(out, map[string]any{"type": "input_image", "image_url": url})
		default:
			if t, _ := pm["text"].(string); t != "" {
				out = append(out, map[string]any{"type": "input_text", "text": t})
			}
		}
	}
	if len(out) == 1 {
		if m, ok := out[0].(map[string]any); ok && m["type"] == "input_text" {
			return m["text"]
		}
	}
	return out
}

func chatContentText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	texts := []string{}
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		if t, _ := pm["text"].(string); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n")
}

// chatToolsToResponses accepts chat function tools and already
// Responses-shaped function tools. Returns nil when no tools given.
func chatToolsToResponses(v any) []any {
	list, _ := v.([]any)
	if len(list) == 0 {
		return nil
	}
	out := []any{}
	for _, t := range list {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if tm["type"] == "function" && tm["name"] != nil {
			out = append(out, tm)
			continue
		}
		fn, _ := tm["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		tool := map[string]any{"type": "function", "name": name}
		if d, _ := fn["description"].(string); d != "" {
			tool["description"] = d
		}
		if p := fn["parameters"]; p != nil {
			tool["parameters"] = p
		}
		out = append(out, tool)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func chatChoiceToResponses(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if t["type"] == "function" {
			if fn, ok := t["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					return map[string]any{"type": "function", "name": name}
				}
			}
		}
		return t
	default:
		return nil
	}
}

// responsesToChat folds a Responses object into a chat completion.
// function_call items become tool_calls, call_id survives as the tool id.
func responsesToChat(up map[string]any, model string) map[string]any {
	texts := []string{}
	calls := []any{}
	items, _ := up["output"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		switch m["type"] {
		case "message":
			content, _ := m["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				if cm["type"] == "output_text" {
					if t, _ := cm["text"].(string); t != "" {
						texts = append(texts, t)
					}
				}
			}
		case "function_call":
			callID, _ := m["call_id"].(string)
			name, _ := m["name"].(string)
			args, _ := m["arguments"].(string)
			if args == "" {
				args = "{}"
			}
			calls = append(calls, map[string]any{
				"id": callID, "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			})
		}
	}
	finish := "stop"
	if status, _ := up["status"].(string); status == "incomplete" {
		reason := ""
		if det, ok := up["incomplete_details"].(map[string]any); ok {
			reason, _ = det["reason"].(string)
		}
		if reason == "max_output_tokens" {
			finish = "length"
		}
	}
	msg := map[string]any{"role": "assistant", "content": strings.Join(texts, "")}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
		finish = "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	if u, _ := up["usage"].(map[string]any); u != nil {
		usage["prompt_tokens"] = u["input_tokens"]
		usage["completion_tokens"] = u["output_tokens"]
		usage["total_tokens"] = u["total_tokens"]
	}
	return map[string]any{
		"id": up["id"], "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
		"usage": usage,
	}
}

// Object mapping helpers below. SSE stream translation lives in stream.go.
func num(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
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
