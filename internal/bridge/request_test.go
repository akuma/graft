package bridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestToResponsesTextImageAndTool(t *testing.T) {
	req, err := ToResponses(map[string]any{
		"model":      "grok-4.7[1m]",
		"max_tokens": 32,
		"stream":     true,
		"system":     []any{map[string]any{"type": "text", "text": "be brief", "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aaaa"}},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]any{"path": "a.go"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "package a"},
			}},
		},
		"tools": []any{
			map[string]any{"name": "Read", "description": "read", "input_schema": map[string]any{"type": "object"}, "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "web_search_20250305", "name": "web_search"},
		},
		"tool_choice":        map[string]any{"type": "auto"},
		"output_config":      map[string]any{"effort": "high"},
		"temperature":        1,
		"context_management": map[string]any{"edits": []any{}},
		"stop_sequences":     []any{"STOP"},
	}, Options{AcceptsImages: true, Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Body["model"] != "grok-4.7" {
		t.Fatalf("model = %#v", req.Body["model"])
	}
	if _, ok := req.Body["context_management"]; ok || req.Body["temperature"] != nil {
		t.Fatalf("dropped fields leaked: %#v", req.Body)
	}
	if strings.Join(req.Stop, ",") != "STOP" {
		t.Fatalf("stop = %#v", req.Stop)
	}
	reasoning := req.Body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != nil {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	include := req.Body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", include)
	}
	tools := req.Body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools = %#v", tools)
	}
	input := req.Body["input"].([]any)
	var sawImage, sawCall, sawOutput bool
	for _, raw := range input {
		item := raw.(map[string]any)
		if item["role"] == "user" {
			for _, partRaw := range item["content"].([]any) {
				part := partRaw.(map[string]any)
				if part["type"] == "input_image" {
					sawImage = true
					if part["detail"] != "high" || part["image_url"] != "data:image/png;base64,aaaa" {
						t.Fatalf("image = %#v", part)
					}
				}
			}
		}
		if item["type"] == "function_call" {
			sawCall = true
			if item["name"] != "Read" || item["arguments"] != `{"path":"a.go"}` {
				t.Fatalf("call = %#v", item)
			}
		}
		if item["type"] == "function_call_output" {
			sawOutput = true
			if item["output"] != "package a" {
				t.Fatalf("output = %#v", item)
			}
		}
	}
	if !sawImage || !sawCall || !sawOutput {
		t.Fatalf("image %v call %v output %v", sawImage, sawCall, sawOutput)
	}
	if req.Body["instructions"] != "be brief" {
		t.Fatalf("instructions = %#v", req.Body["instructions"])
	}
}

func TestReasoningReplayKeepsCiphertextAndDropsContent(t *testing.T) {
	resp := FromResponse(map[string]any{
		"id":     "resp_1",
		"model":  "grok-4.7-build",
		"status": "completed",
		"output": []any{
			map[string]any{
				"type":              "reasoning",
				"encrypted_content": "cipher",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "hmm"}},
			},
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "pong"},
			}},
		},
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 4},
	})
	blocks := resp["content"].([]any)
	thinking := blocks[0].(map[string]any)
	if thinking["thinking"] != "hmm" || !strings.HasPrefix(thinking["signature"].(string), sigPrefix) {
		t.Fatalf("thinking = %#v", thinking)
	}
	again, err := ToResponses(map[string]any{
		"model":      "grok-4.7",
		"max_tokens": 8,
		"messages":   []any{map[string]any{"role": "assistant", "content": blocks}},
	}, Options{Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	for _, raw := range again.Body["input"].([]any) {
		got := raw.(map[string]any)
		if got["type"] == "reasoning" {
			item = got
		}
	}
	if item["encrypted_content"] != "cipher" {
		t.Fatalf("encrypted = %#v", item["encrypted_content"])
	}
	if _, ok := item["content"]; ok {
		t.Fatalf("content must stay absent: %#v", item)
	}
	summary := item["summary"].([]any)[0].(map[string]any)
	if summary["text"] != "hmm" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRedactedThinkingReplayKeepsEmptySummary(t *testing.T) {
	sig := encodeSig("cipher")
	req, err := ToResponses(map[string]any{
		"model":      "grok-4.7",
		"max_tokens": 8,
		"messages": []any{map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": sig},
			map[string]any{"type": "redacted_thinking", "data": sig},
		}}},
	}, Options{Efforts: []string{"high"}, DefaultEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, raw := range req.Body["input"].([]any) {
		item := raw.(map[string]any)
		if item["type"] != "reasoning" {
			continue
		}
		n++
		if item["encrypted_content"] != "cipher" {
			t.Fatalf("encrypted = %#v", item["encrypted_content"])
		}
		summary, ok := item["summary"].([]any)
		if !ok || len(summary) != 0 {
			t.Fatalf("summary = %#v", item["summary"])
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"summary":[]`)) {
			t.Fatalf("summary encoded as %s", encoded)
		}
	}
	if n != 2 {
		t.Fatalf("reasoning items = %d", n)
	}
}

func TestUnknownEffortFallsBackAndBlindModelDropsImages(t *testing.T) {
	req, err := ToResponses(map[string]any{
		"model":         "deepseek-v4-pro",
		"max_tokens":    8,
		"output_config": map[string]any{"effort": "medium"},
		"thinking":      map[string]any{"type": "disabled"},
		"temperature":   0.2,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aaaa"}},
		}}},
	}, Options{AcceptsImages: false, Efforts: []string{"low", "high", "max"}, DefaultEffort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Body["reasoning"]; ok {
		t.Fatal("disabled thinking still set reasoning")
	}
	if req.Body["temperature"] != 0.2 {
		t.Fatalf("temperature = %#v", req.Body["temperature"])
	}
	input := req.Body["input"].([]any)[0].(map[string]any)
	part := input["content"].([]any)[0].(map[string]any)
	if part["text"] != "[image omitted]" {
		t.Fatalf("part = %#v", part)
	}

	withEffort, err := ToResponses(map[string]any{
		"model":         "grok-4.7",
		"max_tokens":    8,
		"output_config": map[string]any{"effort": "max"},
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
	}, Options{Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if withEffort.Body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("max is not a grok effort: %#v", withEffort.Body["reasoning"])
	}
}

func TestPreferredEffort(t *testing.T) {
	if PreferredEffort([]string{"low", "high", "max"}) != "max" {
		t.Fatal("deepseek")
	}
	if PreferredEffort([]string{"low", "medium", "high", "xhigh"}) != "high" {
		t.Fatal("grok")
	}
}

func TestFromResponseToolUse(t *testing.T) {
	msg := FromResponse(map[string]any{
		"id": "resp_2",
		"output": []any{map[string]any{
			"type": "function_call", "call_id": "call_9", "name": "Bash", "arguments": `{"cmd":"ls"}`,
		}},
	})
	if msg["stop_reason"] != "tool_use" {
		t.Fatalf("stop = %#v", msg["stop_reason"])
	}
	block := msg["content"].([]any)[0].(map[string]any)
	raw, _ := json.Marshal(block["input"])
	if block["id"] != "call_9" || string(raw) != `{"cmd":"ls"}` {
		t.Fatalf("block = %#v", block)
	}
}
