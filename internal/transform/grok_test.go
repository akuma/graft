package transform

import "testing"

func base() map[string]any {
	return map[string]any{
		"model":   "grok-4.7",
		"include": []any{"reasoning.encrypted_content"},
		"input":   []any{map[string]any{"type": "message", "role": "user", "content": "hi"}},
		"tools":   []any{},
	}
}

func TestNamespaceToolsAreDropped(t *testing.T) {
	body := base()
	body["tools"] = []any{
		map[string]any{"type": "function", "name": "exec_command"},
		map[string]any{"type": "namespace", "name": "multi_agent_v1", "tools": []any{}},
	}
	got := ForGrok(body)
	tools := got.Body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "exec_command" {
		t.Fatalf("tools = %#v", tools)
	}
	if !hasNote(got.Notes, "r1:drop-namespace:multi_agent_v1") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestWebSearchFieldsAreStripped(t *testing.T) {
	body := base()
	body["tools"] = []any{map[string]any{"type": "web_search", "external_web_access": false}}
	got := ForGrok(body)
	tool := got.Body["tools"].([]any)[0].(map[string]any)
	if len(tool) != 1 || tool["type"] != "web_search" {
		t.Fatalf("tool = %#v", tool)
	}
}

func TestWebSearchWithoutExtrasIsUntouched(t *testing.T) {
	body := base()
	body["tools"] = []any{map[string]any{"type": "web_search"}}
	got := ForGrok(body)
	if len(got.Notes) != 0 {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestNullReasoningContentIsDropped(t *testing.T) {
	body := base()
	reasoning := map[string]any{
		"type":              "reasoning",
		"id":                "rs_1",
		"summary":           []any{map[string]any{"type": "summary_text", "text": "plan"}},
		"content":           nil,
		"encrypted_content": "blob",
	}
	plain := map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "kept"}
	filled := map[string]any{"type": "reasoning", "content": []any{map[string]any{"type": "reasoning_text", "text": "full"}}}
	body["input"] = []any{
		map[string]any{"type": "message", "content": nil},
		reasoning,
		plain,
		filled,
		map[string]any{"type": "function_call"},
	}
	got := ForGrok(body)
	items := got.Body["input"].([]any)
	if len(items) != 5 {
		t.Fatalf("input len = %d", len(items))
	}
	gotReasoning := items[1].(map[string]any)
	if _, ok := gotReasoning["content"]; ok {
		t.Fatalf("null content kept: %#v", gotReasoning)
	}
	if gotReasoning["encrypted_content"] != "blob" || gotReasoning["id"] != "rs_1" {
		t.Fatalf("reasoning = %#v", gotReasoning)
	}
	if _, ok := plain["content"]; ok || items[2].(map[string]any)["encrypted_content"] != "kept" {
		t.Fatalf("plain reasoning changed: %#v", items[2])
	}
	if items[3].(map[string]any)["content"] == nil {
		t.Fatal("non-null reasoning content was dropped")
	}
	if _, ok := items[0].(map[string]any)["content"]; !ok {
		t.Fatal("null content on a message was dropped")
	}
	if _, ok := reasoning["content"]; !ok {
		t.Fatal("input reasoning item was mutated")
	}
	if !hasNote(got.Notes, "r3:drop-null-reasoning-content:1") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestIncludeIsPreserved(t *testing.T) {
	got := ForGrok(base())
	inc := got.Body["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", inc)
	}
}

func TestUnknownToolTypesAreDropped(t *testing.T) {
	body := base()
	body["tools"] = []any{
		map[string]any{"type": "function", "name": "ok"},
		map[string]any{"type": "custom", "name": "apply_patch"},
	}
	got := ForGrok(body)
	if len(got.Body["tools"].([]any)) != 1 {
		t.Fatalf("tools = %#v", got.Body["tools"])
	}
}

func TestToolChoiceDroppedWhenToolsEmpty(t *testing.T) {
	body := base()
	body["tool_choice"] = "auto"
	body["parallel_tool_calls"] = true
	got := ForGrok(body)
	if _, ok := got.Body["tool_choice"]; ok {
		t.Fatalf("tool_choice = %#v", got.Body["tool_choice"])
	}
	if got.Body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v", got.Body["parallel_tool_calls"])
	}
	if !hasNote(got.Notes, "r4:drop-tool-choice") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestToolChoiceDroppedWhenToolsMissing(t *testing.T) {
	body := base()
	delete(body, "tools")
	body["tool_choice"] = "none"
	got := ForGrok(body)
	if _, ok := got.Body["tool_choice"]; ok {
		t.Fatalf("tool_choice = %#v", got.Body["tool_choice"])
	}
}

func TestToolChoiceDroppedWhenOnlyNamespacesRemain(t *testing.T) {
	body := base()
	body["tools"] = []any{map[string]any{"type": "namespace", "name": "mcp__codegraph"}}
	body["tool_choice"] = "auto"
	got := ForGrok(body)
	if len(got.Body["tools"].([]any)) != 0 {
		t.Fatalf("tools = %#v", got.Body["tools"])
	}
	if _, ok := got.Body["tool_choice"]; ok {
		t.Fatal("tool_choice survived an empty tool list")
	}
}

func TestToolChoiceKeptWhenToolsRemain(t *testing.T) {
	body := base()
	body["tools"] = []any{
		map[string]any{"type": "function", "name": "exec_command"},
		map[string]any{"type": "namespace", "name": "mcp__codegraph"},
	}
	body["tool_choice"] = "auto"
	got := ForGrok(body)
	if got.Body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", got.Body["tool_choice"])
	}
	if hasNote(got.Notes, "r4:drop-tool-choice") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestInputIsNotMutated(t *testing.T) {
	body := base()
	body["tools"] = []any{map[string]any{"type": "namespace", "name": "web"}}
	ForGrok(body)
	if len(body["tools"].([]any)) != 1 {
		t.Fatal("input was mutated")
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if n == want {
			return true
		}
	}
	return false
}
