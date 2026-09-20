package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatToResponsesToolRoundTrip(t *testing.T) {
	in := map[string]any{
		"model": "gpt-5-codex",
		"messages": []any{
			map[string]any{"role": "system", "content": "Be brief."},
			map[string]any{"role": "user", "content": "What time is it?"},
			map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "clock", "arguments": "{}"},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "noon"},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "clock", "description": "time",
				"parameters": map[string]any{"type": "object"},
			},
		}},
		"tool_choice": "auto",
		"max_tokens":  float64(100),
	}
	out, err := chatToResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	if out["instructions"] != "Be brief." {
		t.Fatalf("instructions: %v", out["instructions"])
	}
	input, _ := out["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input items: %d", len(input))
	}
	fn, _ := input[1].(map[string]any)
	if fn["type"] != "function_call" || fn["call_id"] != "call_1" || fn["name"] != "clock" {
		t.Fatalf("function_call: %v", fn)
	}
	fbo, _ := input[2].(map[string]any)
	if fbo["type"] != "function_call_output" || fbo["output"] != "noon" {
		t.Fatalf("function_call_output: %v", fbo)
	}
	tools, _ := out["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "clock" {
		t.Fatalf("tools: %v", tools)
	}
	if out["max_output_tokens"] != 100 {
		t.Fatalf("max_output_tokens: %v", out["max_output_tokens"])
	}
}

func TestResponsesToChatTools(t *testing.T) {
	up := map[string]any{
		"id": "resp-1", "status": "completed",
		"output": []any{
			map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": "checking"},
			}},
			map[string]any{"type": "function_call", "call_id": "call_9",
				"name": "clock", "arguments": "{}"},
		},
		"usage": map[string]any{"input_tokens": 5, "output_tokens": 7, "total_tokens": 12},
	}
	got := responsesToChat(up, "gpt-5-codex")
	choice, _ := got["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish: %v", choice["finish_reason"])
	}
	msg, _ := choice["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	if call["id"] != "call_9" {
		t.Fatalf("call id lost: %v", calls)
	}
	usage, _ := got["usage"].(map[string]any)
	if usage["prompt_tokens"] != 5 || usage["total_tokens"] != 12 {
		t.Fatalf("usage: %v", usage)
	}
}

func TestResponsesToChatLength(t *testing.T) {
	up := map[string]any{
		"id": "resp-2", "status": "incomplete",
		"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output":             []any{},
	}
	got := responsesToChat(up, "m")
	choice, _ := got["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "length" {
		t.Fatalf("finish: %v", choice["finish_reason"])
	}
}

func TestStreamTranslatorFixture(t *testing.T) {
	tr := newChatStreamTranslator("gpt-5-codex")
	frames := []string{
		`{"type":"response.created","response":{"id":"resp-7"}}`,
		`{"type":"response.output_text.delta","delta":"Hi"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"x","call_id":"call_3","name":"clock"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"call_3","delta":"{}"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}
	var chunks []string
	var done bool
	for _, f := range frames {
		var lines []string
		lines, done = tr.translate(f)
		chunks = append(chunks, lines...)
	}
	if !done {
		t.Fatal("stream never finished")
	}
	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, `"content":"Hi"`) {
		t.Fatalf("text delta missing: %s", joined)
	}
	if !strings.Contains(joined, `"name":"clock"`) || !strings.Contains(joined, `call_3`) {
		t.Fatalf("tool open missing: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish missing: %s", joined)
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(chunks[len(chunks)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last["id"] != "resp-7" {
		t.Fatalf("id not carried: %v", last["id"])
	}
}

func TestStreamFailedIsError(t *testing.T) {
	tr := newChatStreamTranslator("m")
	lines, done := tr.translate(`{"type":"response.failed","response":{"error":{"message":"bad context"}}}`)
	if !done || len(lines) != 1 || !strings.Contains(lines[0], "bad context") {
		t.Fatalf("lines=%v done=%v", lines, done)
	}
}
