package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const collectFixture = `event: response.created
data: {"type":"response.created","response":{"id":"resp-9"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"there"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"clock"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"a\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"1}"}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":9,"total_tokens":12}}}
`

func TestCollectResponses(t *testing.T) {
	up, err := collectResponses(strings.NewReader(collectFixture), "m")
	if err != nil {
		t.Fatal(err)
	}
	if up["id"] != "resp-9" || up["status"] != "completed" {
		t.Fatalf("meta: %v %v", up["id"], up["status"])
	}
	items, _ := up["output"].([]any)
	if len(items) != 2 {
		t.Fatalf("items: %d", len(items))
	}
	msg, _ := items[0].(map[string]any)
	content, _ := msg["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["text"] != "Hello there" {
		t.Fatalf("text: %v", part["text"])
	}
	fn, _ := items[1].(map[string]any)
	if fn["type"] != "function_call" || fn["call_id"] != "call_1" || fn["arguments"] != `{"a":1}` {
		t.Fatalf("call: %v", fn)
	}
	usage, _ := up["usage"].(map[string]any)
	if usage["total_tokens"] != float64(12) {
		t.Fatalf("usage: %v", usage)
	}
}

func TestCollectFailedIsError(t *testing.T) {
	sse := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"kaput\"}}}\n\n"
	_, err := collectResponses(strings.NewReader(sse), "m")
	if err == nil || !strings.Contains(err.Error(), "kaput") {
		t.Fatalf("err: %v", err)
	}
}

func TestTranslatorDualKey(t *testing.T) {
	tr := newChatStreamTranslator("m")
	open, _ := tr.translate(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"clock"}}`)
	app, _ := tr.translate(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{}"}`)
	if len(open) != 1 || len(app) != 1 {
		t.Fatalf("open=%v app=%v", open, app)
	}
	idx := func(raw string) int {
		var c map[string]any
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		choices, _ := c["choices"].([]any)
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		call, _ := calls[0].(map[string]any)
		i, _ := call["index"].(float64)
		return int(i)
	}
	if idx(open[0]) != idx(app[0]) {
		t.Fatalf("args landed on a second tool: %v vs %v", open[0], app[0])
	}
}

func TestChatNonStreamCollects(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["stream"] != true {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"detail":"Stream must be set to true"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.ReplaceAll(collectFixture, "event: ", "event: ")))
	}))
	defer up.Close()

	s := testServerWithAccount(t, up.Client())
	s.baseURL = up.URL
	in := map[string]any{"model": "m", "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
	}}
	respBody, err := chatToResponses(in)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	s.chatNonStream(rec, req, "m", respBody)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status: %d", res.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	choices, _ := got["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish: %v", choice["finish_reason"])
	}
	msg, _ := choice["message"].(map[string]any)
	if msg["content"] != "Hello there" {
		t.Fatalf("content: %v", msg["content"])
	}
}
