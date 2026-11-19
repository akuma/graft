// Package transform rewrites Responses requests a specific upstream rejects.
//
// Each rule exists because cli-chat-proxy.grok.com returned an error for an
// unmodified Codex request. A transport may change only those bytes.
//
//	R1 namespace tools   -> 422 unknown variant `namespace`
//	R2 web_search extras -> 400 Argument not supported: external_web_access
//	R3 echoed reasoning  -> 400 Could not decode the compaction blob
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

// ForGrok applies R1..R3. The input map is not modified.
// include: ["reasoning.encrypted_content"] is left in place; only echoed
// reasoning items are rejected.
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
		out["input"] = dropReasoning(items, &notes)
	}
	return Result{Body: out, Notes: notes}
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

func dropReasoning(items []any, notes *[]string) []any {
	kept := make([]any, 0, len(items))
	dropped := 0
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && str(item["type"]) == "reasoning" {
			dropped++
			continue
		}
		kept = append(kept, raw)
	}
	if dropped > 0 {
		*notes = append(*notes, "r3:drop-reasoning-items:"+itoa(dropped))
	}
	return kept
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
