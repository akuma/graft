package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestPrepareKeepsGrokReasoningShape(t *testing.T) {
	p := &config.Provider{
		Name: "grok",
		Models: []config.Model{{
			Slug: "grok-4.7", Input: []string{"text", "image"},
			Reasoning: []string{"low", "medium", "high", "xhigh"},
		}},
	}
	body, stop, _, err := Prepare(p, map[string]any{
		"model":      "grok-4.7",
		"max_tokens": 16,
		"messages": []any{map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "hmm", "signature": "not-ours"},
			map[string]any{"type": "text", "text": "ok"},
		}}},
		"temperature": 1,
		"tools":       []any{map[string]any{"type": "namespace", "name": "mcp"}},
		"tool_choice": map[string]any{"type": "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stop) != 0 {
		t.Fatalf("stop = %#v", stop)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("effort = %#v", reasoning)
	}
	if _, ok := body["temperature"]; ok {
		t.Fatal("temperature survived a reasoning request")
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("namespace tool survived")
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatal("orphan tool_choice survived")
	}
	item := body["input"].([]any)[0].(map[string]any)
	if item["type"] != "reasoning" {
		t.Fatalf("item = %#v", item)
	}
	if _, ok := item["content"]; ok || item["encrypted_content"] != nil {
		t.Fatalf("foreign signature was replayed: %#v", item)
	}
}

func TestHandlerStreamsTranslatedResponse(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_1", "status": "completed",
				"usage":  map[string]any{"input_tokens": 2, "output_tokens": 1},
				"output": []any{map[string]any{"id": "m", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "pong"}}}},
			},
		})
		io.WriteString(w, "data: "+string(payload)+"\n\n")
	}))
	defer upstream.Close()
	p := &config.Provider{
		Name: "grok", Transport: "thin", Upstream: upstream.URL + "/v1",
		Models: []config.Model{{
			Slug: "grok-4.7", Input: []string{"text", "image"},
			Reasoning: []string{"low", "medium", "high", "xhigh"},
		}},
	}
	gw := httptest.NewServer(Handler(p))
	defer gw.Close()

	res, err := http.Post(gw.URL+"/v1/messages?beta=true", "application/json", strings.NewReader(`{
		"model":"grok-4.7","max_tokens":16,"stream":true,
		"messages":[{"role":"user","content":"ping"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"text":"pong"`) || !strings.Contains(string(raw), "message_stop") {
		t.Fatalf("status %d body %s", res.StatusCode, raw)
	}
	if got["model"] != "grok-4.7" || got["stream"] != true {
		t.Fatalf("upstream = %#v", got)
	}
}

func TestCountTokensAndHealth(t *testing.T) {
	p := &config.Provider{Name: "grok", Upstream: "http://127.0.0.1"}
	gw := httptest.NewServer(Handler(p))
	defer gw.Close()
	res, err := http.Post(gw.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var count struct {
		Input int `json:"input_tokens"`
	}
	if err := json.NewDecoder(res.Body).Decode(&count); err != nil || count.Input < 1 {
		t.Fatalf("count = %+v err %v", count, err)
	}
	health, err := http.Get(gw.URL + "/graft/health")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	var body struct {
		Transport string `json:"transport"`
	}
	if err := json.NewDecoder(health.Body).Decode(&body); err != nil || body.Transport != "claude" {
		t.Fatalf("health = %+v err %v", body, err)
	}
}
