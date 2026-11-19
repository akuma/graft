// Package transform rewrites Responses requests a specific upstream rejects.
//
// Each rule exists because cli-chat-proxy.grok.com returned an error for an
// unmodified Codex request. A transport may change only those bytes.
//
//	R1 namespace tools   -> 422 unknown variant `namespace`
//	R2 web_search extras -> 400 Argument not supported: external_web_access
//	R3 null reasoning content -> 400 Could not decode the compaction blob
//	R4 orphan tool_choice -> 400 tool_choice set but no tools were specified
package transform

import "sort"

// Tool types the Grok responses proxy accepts, taken from its own 422 message.
var grokToolTypes = map[string]bool{
	"function": true, "web_search": true, "x_search": true,
	"image_generation": true, "collections_search": true, "file_search": true,
	"code_execution": true, "code_interpreter": true, "mcp": true,
	"shell": true, "tool_search": true,
}

// Result is a rewritten request body and the rules that fired.
type Result struct {
	Body  map[string]any
	Notes []string
}

// ForGrok applies R1..R4. The input map is not modified.
// include: ["reasoning.encrypted_content"] is left in place. A replayed
// reasoning item stays; only its null content channel is removed.
func ForGrok(body map[string]any) Result {
	out := clone(body)
	var notes []string
	if tools, ok := out["tools"].([]any); ok {
		tools = dropNamespaces(tools, &notes)
		for i, raw := range tools {
			tool, ok := raw.(map[string]any)
			if ok && str(tool["type"]) == "web_search" {
				tools[i] = stripWebSearch(tool, &notes)
			}
		}
		out["tools"] = dropUnsupported(tools, &notes)
	}
	if items, ok := out["input"].([]any); ok {
		out["input"] = stripNullReasoningContent(items, &notes)
	}
	// Codex local compaction sends tools: [] with tool_choice: "auto".
	// The same 400 happens when every tool was a dropped namespace.
	if _, hasChoice := out["tool_choice"]; hasChoice && !hasTools(out) {
		delete(out, "tool_choice")
		notes = append(notes, "r4:drop-tool-choice")
	}
	return Result{Body: out, Notes: notes}
}

func hasTools(body map[string]any) bool {
	tools, ok := body["tools"].([]any)
	return ok && len(tools) > 0
}

func dropNamespaces(tools []any, notes *[]string) []any {
	kept := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && str(tool["type"]) == "namespace" {
			*notes = append(*notes, "r1:drop-namespace:"+str(tool["name"]))
			continue
		}
		kept = append(kept, raw)
	}
	return kept
}

func stripWebSearch(tool map[string]any, notes *[]string) map[string]any {
	if len(tool) <= 1 {
		return tool
	}
	var keys []string
	for k := range tool {
		if k != "type" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	*notes = append(*notes, "r2:strip-web-search-fields:"+join(keys))
	return map[string]any{"type": "web_search"}
}

// stripNullReasoningContent deletes "content": null on reasoning items.
// Codex writes that null for an absent channel. xAI rejects it and names the
// sibling encrypted_content. The item, its summary, and the blob stay.
func stripNullReasoningContent(items []any, notes *[]string) []any {
	out := make([]any, len(items))
	stripped := 0
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || str(item["type"]) != "reasoning" {
			out[i] = raw
			continue
		}
		content, has := item["content"]
		if !has || content != nil {
			out[i] = raw
			continue
		}
		copied := make(map[string]any, len(item))
		for k, v := range item {
			if k != "content" {
				copied[k] = v
			}
		}
		out[i] = copied
		stripped++
	}
	if stripped > 0 {
		*notes = append(*notes, "r3:drop-null-reasoning-content:"+itoa(stripped))
	}
	return out
}

func dropUnsupported(tools []any, notes *[]string) []any {
	kept := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		if grokToolTypes[str(tool["type"])] {
			kept = append(kept, raw)
			continue
		}
		*notes = append(*notes, "drop-unsupported-tool:"+str(tool["type"])+":"+str(tool["name"]))
	}
	return kept
}

func clone(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func join(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
