// Package bridge translates Anthropic Messages into the Responses API and back.
//
// Claude Code posts /v1/messages. DeepSeek accepts that directly. The Grok
// subscription proxy does not, so a thin provider is translated here and then
// passed through transform.ForGrok. Only bytes that have a Responses equivalent
// are kept. Cache control, context management, and server-side Anthropic tools
// are dropped.
package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// sigPrefix marks a thinking signature that carries Grok encrypted_content.
// Anthropic signatures are a different ciphertext and are not replayed as one.
const sigPrefix = "graft.ec.v1."

// Request is a Responses body plus the stop sequences Responses cannot express.
type Request struct {
	Body map[string]any
	Stop []string
}

// Options describes the model the request is aimed at.
type Options struct {
	AcceptsImages bool
	Efforts       []string
	DefaultEffort string
}

// PreferredEffort is the Claude Code effort for a configured model.
// max wins when it is listed, which is DeepSeek's Claude Code guide.
// Otherwise high, then the first listed level.
func PreferredEffort(levels []string) string {
	for _, level := range levels {
		if level == "max" {
			return "max"
		}
	}
	for _, level := range levels {
		if level == "high" {
			return "high"
		}
	}
	if len(levels) > 0 && levels[0] != "" {
		return levels[0]
	}
	return "high"
}

// ToResponses converts one Messages request. The input map is not modified.
func ToResponses(msg map[string]any, opt Options) (Request, error) {
	if msg == nil {
		return Request{}, fmt.Errorf("empty messages request")
	}
	out := map[string]any{}
	model := stripContextSuffix(str(msg["model"]))
	if model == "" {
		return Request{}, fmt.Errorf("messages request has no model")
	}
	out["model"] = model
	if n := intOf(msg["max_tokens"]); n > 0 {
		out["max_output_tokens"] = n
	} else {
		out["max_output_tokens"] = 8192
	}
	if stream, ok := msg["stream"].(bool); ok {
		out["stream"] = stream
	}

	var instructions []string
	instructions = append(instructions, systemText(msg["system"])...)
	input, err := convertMessages(msg["messages"], opt.AcceptsImages, &instructions)
	if err != nil {
		return Request{}, err
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(input) > 0 {
		out["input"] = input
	} else {
		out["input"] = []any{}
	}

	if tools := convertTools(msg["tools"]); len(tools) > 0 {
		out["tools"] = tools
		if choice := convertToolChoice(msg["tool_choice"]); choice != nil {
			out["tool_choice"] = choice
		}
	}

	if !thinkingDisabled(msg["thinking"]) {
		effort := coerceEffort(requestedEffort(msg), opt.Efforts, opt.DefaultEffort)
		out["reasoning"] = map[string]any{"effort": effort}
		out["include"] = []any{"reasoning.encrypted_content"}
	} else {
		// Reasoning models reject sampling parameters. A disabled thinking
		// request is the one case that still carries them.
		if n, ok := number(msg["temperature"]); ok {
			out["temperature"] = n
		}
		if n, ok := number(msg["top_p"]); ok {
			out["top_p"] = n
		}
	}

	return Request{Body: out, Stop: stringList(msg["stop_sequences"])}, nil
}

// FromResponse converts a non-streaming Responses result into a Messages result.
func FromResponse(resp map[string]any) map[string]any {
	if resp == nil {
		resp = map[string]any{}
	}
	id := str(resp["id"])
	if id == "" {
		id = "msg_graft"
	}
	var content []any
	sawTool := false
	for _, raw := range slice(resp["output"]) {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch str(item["type"]) {
		case "reasoning":
			content = append(content, thinkingBlock(item))
		case "message":
			if text := messageText(item); text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
		case "function_call":
			sawTool = true
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    callID(item),
				"name":  str(item["name"]),
				"input": parseObject(str(item["arguments"])),
			})
		}
	}
	if content == nil {
		content = []any{}
	}
	stop := "end_turn"
	if sawTool {
		stop = "tool_use"
	} else if incompleteMax(resp) {
		stop = "max_tokens"
	}
	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         str(resp["model"]),
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage":         usageOf(resp["usage"]),
	}
}

// EstimateTokens is a stand-in for /v1/messages/count_tokens.
// Claude Code uses its own estimate when that route is missing. Returning a
// rough count keeps the context meter from sitting at zero.
func EstimateTokens(msg map[string]any) int {
	payload, err := marshal(msg)
	if err != nil || len(payload) < 4 {
		return 1
	}
	n := len(payload) / 4
	if n < 1 {
		return 1
	}
	return n
}

func convertMessages(raw any, images bool, instructions *[]string) ([]any, error) {
	var input []any
	for _, rawMsg := range slice(raw) {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		role := str(msg["role"])
		blocks := contentBlocks(msg["content"])
		switch role {
		case "system", "developer":
			*instructions = append(*instructions, blockText(blocks)...)
		case "user":
			input = append(input, convertUser(blocks, images)...)
		case "assistant":
			input = append(input, convertAssistant(blocks)...)
		default:
			return nil, fmt.Errorf("unsupported message role %q", role)
		}
	}
	return input, nil
}

func convertUser(blocks []map[string]any, images bool) []any {
	var input []any
	var parts []any
	var imagesAfter []any
	flush := func() {
		if len(parts) == 0 {
			return
		}
		input = append(input, map[string]any{"role": "user", "content": parts})
		parts = nil
	}
	for _, block := range blocks {
		switch str(block["type"]) {
		case "", "text":
			if text := str(block["text"]); text != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": text})
			}
		case "image":
			if images {
				if part, ok := imagePart(block); ok {
					parts = append(parts, part)
				}
			} else if text := str(block["text"]); text != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": text})
			} else {
				parts = append(parts, map[string]any{"type": "input_text", "text": "[image omitted]"})
			}
		case "tool_result":
			flush()
			output, pics := toolResultOutput(block, images)
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": str(block["tool_use_id"]),
				"output":  output,
			})
			imagesAfter = append(imagesAfter, pics...)
		}
	}
	flush()
	// function_call_output is text. Pixels from a tool result follow it as a
	// user turn so a Read of an image still reaches the model.
	if len(imagesAfter) > 0 {
		input = append(input, map[string]any{"role": "user", "content": imagesAfter})
	}
	return input
}

func convertAssistant(blocks []map[string]any) []any {
	var input []any
	var text []any
	flush := func() {
		if len(text) == 0 {
			return
		}
		input = append(input, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": text,
		})
		text = nil
	}
	for _, block := range blocks {
		switch str(block["type"]) {
		case "thinking", "redacted_thinking":
			flush()
			if item, ok := reasoningItem(block); ok {
				input = append(input, item)
			}
		case "", "text":
			if t := str(block["text"]); t != "" {
				text = append(text, map[string]any{"type": "output_text", "text": t})
			}
		case "tool_use":
			flush()
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   str(block["id"]),
				"name":      str(block["name"]),
				"arguments": jsonString(block["input"]),
			})
		}
	}
	flush()
	return input
}

func reasoningItem(block map[string]any) (map[string]any, bool) {
	item := map[string]any{"type": "reasoning"}
	text := str(block["thinking"])
	if text == "" {
		text = str(block["text"])
	}
	if text != "" {
		item["summary"] = []any{map[string]any{"type": "summary_text", "text": text}}
	}
	// Claude Code replays a sealed turn as redacted_thinking and puts the
	// signature in data. An encrypted_content without summary is what Grok
	// rejects as "Could not decode the compaction blob".
	sig := str(block["signature"])
	if sig == "" {
		sig = str(block["data"])
	}
	if enc, ok := decodeSig(sig); ok && enc != "" {
		item["encrypted_content"] = enc
		if item["summary"] == nil {
			item["summary"] = []any{}
		}
	}
	if text == "" && item["encrypted_content"] == nil {
		return nil, false
	}
	return item, true
}

func thinkingBlock(item map[string]any) map[string]any {
	block := map[string]any{
		"type":     "thinking",
		"thinking": summaryText(item),
	}
	if enc := str(item["encrypted_content"]); enc != "" {
		block["signature"] = encodeSig(enc)
	} else {
		block["signature"] = "graft.nosig.v1"
	}
	return block
}

func convertTools(raw any) []any {
	var tools []any
	for _, rawTool := range slice(raw) {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		kind := str(tool["type"])
		if kind != "" && kind != "custom" && kind != "function" {
			continue
		}
		name := str(tool["name"])
		if name == "" {
			continue
		}
		params := tool["input_schema"]
		if params == nil {
			params = tool["parameters"]
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		fn := map[string]any{"type": "function", "name": name, "parameters": params}
		if desc := str(tool["description"]); desc != "" {
			fn["description"] = desc
		}
		tools = append(tools, fn)
	}
	return tools
}

func convertToolChoice(raw any) any {
	switch choice := raw.(type) {
	case string:
		switch choice {
		case "any":
			return "required"
		case "auto", "none", "required":
			return choice
		default:
			return nil
		}
	case map[string]any:
		switch str(choice["type"]) {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		case "tool":
			name := str(choice["name"])
			if name == "" {
				return "required"
			}
			return map[string]any{"type": "function", "name": name}
		}
	}
	return nil
}

func toolResultOutput(block map[string]any, images bool) (string, []any) {
	var pics []any
	switch content := block["content"].(type) {
	case string:
		return content, nil
	case nil:
		return "", nil
	default:
		var b strings.Builder
		for _, raw := range contentBlocks(content) {
			switch str(raw["type"]) {
			case "", "text":
				if text := str(raw["text"]); text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(text)
				}
			case "image":
				if images {
					if part, ok := imagePart(raw); ok {
						pics = append(pics, part)
					}
				} else if b.Len() == 0 {
					b.WriteString("[image omitted]")
				}
			}
		}
		return b.String(), pics
	}
}

func imagePart(block map[string]any) (map[string]any, bool) {
	source, _ := block["source"].(map[string]any)
	if source == nil {
		return nil, false
	}
	var url string
	switch str(source["type"]) {
	case "base64":
		media := str(source["media_type"])
		if media == "" {
			media = "image/png"
		}
		data := str(source["data"])
		if data == "" {
			return nil, false
		}
		url = "data:" + media + ";base64," + data
	case "url":
		url = str(source["url"])
	default:
		if u := str(source["url"]); u != "" {
			url = u
		}
	}
	if url == "" {
		return nil, false
	}
	return map[string]any{"type": "input_image", "image_url": url, "detail": "high"}, true
}

func systemText(raw any) []string {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	default:
		return blockText(contentBlocks(v))
	}
}

func blockText(blocks []map[string]any) []string {
	var out []string
	for _, block := range blocks {
		if str(block["type"]) != "" && str(block["type"]) != "text" {
			continue
		}
		if text := str(block["text"]); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func contentBlocks(raw any) []map[string]any {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": v}}
	case []any:
		var out []map[string]any
		for _, item := range v {
			block, ok := item.(map[string]any)
			if ok {
				out = append(out, block)
			}
		}
		return out
	default:
		return nil
	}
}

func thinkingDisabled(raw any) bool {
	block, ok := raw.(map[string]any)
	return ok && str(block["type"]) == "disabled"
}

func requestedEffort(msg map[string]any) string {
	if cfg, ok := msg["output_config"].(map[string]any); ok {
		if effort := str(cfg["effort"]); effort != "" {
			return effort
		}
	}
	if effort := str(msg["effort"]); effort != "" {
		return effort
	}
	return ""
}

func coerceEffort(requested string, allowed []string, fallback string) string {
	if requested != "" && (len(allowed) == 0 || contains(allowed, requested)) {
		return requested
	}
	if fallback != "" && (len(allowed) == 0 || contains(allowed, fallback)) {
		return fallback
	}
	if len(allowed) > 0 {
		return allowed[len(allowed)-1]
	}
	if requested != "" {
		return requested
	}
	return "high"
}

func encodeSig(encrypted string) string {
	return sigPrefix + base64.RawURLEncoding.EncodeToString([]byte(encrypted))
}

func decodeSig(sig string) (string, bool) {
	rest, ok := strings.CutPrefix(sig, sigPrefix)
	if !ok || rest == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func summaryText(item map[string]any) string {
	var b strings.Builder
	for _, raw := range slice(item["summary"]) {
		switch part := raw.(type) {
		case string:
			b.WriteString(part)
		case map[string]any:
			b.WriteString(str(part["text"]))
		}
	}
	return b.String()
}

func messageText(item map[string]any) string {
	var b strings.Builder
	for _, raw := range slice(item["content"]) {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text := str(part["text"]); text != "" {
			b.WriteString(text)
		}
	}
	if b.Len() == 0 {
		return str(item["text"])
	}
	return b.String()
}

func callID(item map[string]any) string {
	if id := str(item["call_id"]); id != "" {
		return id
	}
	return str(item["id"])
}

func incompleteMax(resp map[string]any) bool {
	if str(resp["status"]) != "incomplete" {
		return false
	}
	details, _ := resp["incomplete_details"].(map[string]any)
	reason := str(details["reason"])
	return reason == "max_output_tokens" || reason == "max_tokens"
}

func usageOf(raw any) map[string]any {
	usage, _ := raw.(map[string]any)
	return map[string]any{
		"input_tokens":  intOf(usage["input_tokens"]),
		"output_tokens": intOf(usage["output_tokens"]),
	}
}

func parseObject(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v == nil {
		return map[string]any{}
	}
	return v
}

func jsonString(v any) string {
	if s, ok := v.(string); ok {
		if s == "" {
			return "{}"
		}
		return s
	}
	if v == nil {
		return "{}"
	}
	payload, err := marshal(v)
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func stripContextSuffix(model string) string {
	if i := strings.Index(model, "["); i > 0 && strings.HasSuffix(model, "]") {
		return model[:i]
	}
	return model
}

func stringList(raw any) []string {
	var out []string
	for _, item := range slice(raw) {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func slice(raw any) []any {
	items, _ := raw.([]any)
	return items
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
