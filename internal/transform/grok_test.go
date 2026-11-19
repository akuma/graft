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

func TestReasoningItemsAreDropped(t *testing.T) {
	body := base()
	body["input"] = []any{
		map[string]any{"type": "message"},
		map[string]any{"type": "reasoning", "encrypted_content": "blob"},
		map[string]any{"type": "function_call"},
		map[string]any{"type": "function_call_output"},
	}
	got := ForGrok(body)
	items := got.Body["input"].([]any)
	if len(items) != 3 {
		t.Fatalf("input len = %d", len(items))
	}
	if !hasNote(got.Notes, "r3:drop-reasoning-items:1") {
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
