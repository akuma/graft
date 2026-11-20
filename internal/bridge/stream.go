package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// TranslateStream reads a Responses SSE body and writes Anthropic Messages SSE.
// It always ends with message_delta and message_stop when the upstream body
// ends, including on a short read. Claude Code treats a content block that
// never reaches message_delta as a dropped connection.
func TranslateStream(dst io.Writer, src io.Reader, model string, stop []string) error {
	w := &anthropicStream{dst: dst, model: model, stop: stop, hold: map[string]string{}, blocks: map[string]*contentBlock{}}
	err := readSSE(src, w.onEvent)
	if err != nil {
		return err
	}
	w.finish("")
	return nil
}

type contentBlock struct {
	index     int
	kind      string
	closed    bool
	signed    bool
	streamed  bool
	encrypted string
}

type anthropicStream struct {
	dst          io.Writer
	model        string
	stop         []string
	blocks       map[string]*contentBlock
	order        []string
	hold         map[string]string
	started      bool
	finishing    bool
	finished     bool
	sawTool      bool
	id           string
	stopReason   string
	stopSequence any
	inputTokens  int
	outputTokens int
}

func (s *anthropicStream) onEvent(event, data string) error {
	if s.finished || data == "" || data == "[DONE]" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil
	}
	kind := str(payload["type"])
	if kind == "" {
		kind = event
	}
	switch kind {
	case "response.created", "response.in_progress":
		s.noteResponse(payload["response"])
	case "response.output_item.added":
		s.noteResponse(map[string]any{"id": s.id})
		s.onItemAdded(payload)
	case "response.output_text.delta":
		s.addText(itemKey(payload), str(payload["delta"]))
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		s.addThinking(itemKey(payload), str(payload["delta"]))
	case "response.function_call_arguments.delta":
		s.addArgs(itemKey(payload), str(payload["delta"]), "", "")
	case "response.output_item.done":
		s.onItemDone(payload)
	case "response.completed", "response.incomplete":
		s.noteResponse(payload["response"])
		s.finishFrom(payload["response"])
	case "response.failed", "error":
		s.fail(errorMessage(payload))
	}
	return nil
}

func (s *anthropicStream) onItemAdded(payload map[string]any) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return
	}
	id := itemKey(payload)
	if id == "" {
		id = str(item["id"])
	}
	switch str(item["type"]) {
	case "reasoning":
		s.open(id, "thinking", map[string]any{"type": "thinking", "thinking": ""})
		if text := summaryText(item); text != "" {
			s.addThinking(id, text)
		}
	case "function_call":
		s.sawTool = true
		s.open(id, "tool", map[string]any{
			"type":  "tool_use",
			"id":    callID(item),
			"name":  str(item["name"]),
			"input": map[string]any{},
		})
		if args := str(item["arguments"]); args != "" {
			s.addArgs(id, args, callID(item), str(item["name"]))
		}
	}
}

func (s *anthropicStream) onItemDone(payload map[string]any) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return
	}
	id := itemKey(payload)
	if id == "" {
		id = str(item["id"])
	}
	switch str(item["type"]) {
	case "reasoning":
		if text := summaryText(item); text != "" {
			if b := s.blocks[id]; b == nil || !b.streamed {
				s.addThinking(id, text)
			}
		}
		s.stashEncrypted(id, str(item["encrypted_content"]))
	case "message":
		if text := messageText(item); text != "" {
			if b := s.blocks[id]; b == nil || !b.streamed {
				s.addText(id, text)
			}
		}
		s.flushHold(id)
		s.close(id)
	case "function_call":
		s.sawTool = true
		if b := s.blocks[id]; b == nil || !b.streamed {
			s.addArgs(id, str(item["arguments"]), callID(item), str(item["name"]))
		}
		s.close(id)
	}
}

func (s *anthropicStream) finishFrom(raw any) {
	resp, _ := raw.(map[string]any)
	if resp == nil {
		s.finish("")
		return
	}
	s.materialize(resp)
	for _, itemRaw := range slice(resp["output"]) {
		item, ok := itemRaw.(map[string]any)
		if !ok || str(item["type"]) != "reasoning" {
			continue
		}
		s.stashEncrypted(str(item["id"]), str(item["encrypted_content"]))
	}
	if s.sawTool {
		s.stopReason = "tool_use"
	} else if incompleteMax(resp) {
		s.stopReason = "max_tokens"
	}
	s.finish("")
}

// materialize fills blocks that arrived whole on response.completed, which is
// what a non-delta upstream sends. Deltas already streamed are left alone.
func (s *anthropicStream) materialize(resp map[string]any) {
	for i, itemRaw := range slice(resp["output"]) {
		item, ok := itemRaw.(map[string]any)
		if !ok {
			continue
		}
		id := str(item["id"])
		if id == "" {
			id = fmt.Sprintf("out-%d", i)
		}
		switch str(item["type"]) {
		case "reasoning":
			if !s.sawKind("thinking") {
				if text := summaryText(item); text != "" {
					s.addThinking(id, text)
				}
			}
			s.stashEncrypted(id, str(item["encrypted_content"]))
		case "message":
			if !s.sawKind("text") {
				if text := messageText(item); text != "" {
					s.addText(id, text)
				}
			}
		case "function_call":
			if block := s.blocks[id]; block != nil && block.streamed {
				continue
			}
			s.addArgs(id, str(item["arguments"]), callID(item), str(item["name"]))
		}
	}
}

func (s *anthropicStream) sawKind(kind string) bool {
	for _, block := range s.blocks {
		if block.kind == kind && block.streamed {
			return true
		}
	}
	return false
}

func (s *anthropicStream) addText(id, delta string) {
	if delta == "" || s.finished {
		return
	}
	if len(s.stop) == 0 {
		s.emitText(id, delta)
		return
	}
	buf := s.hold[id] + delta
	cut, seq := earliestStop(buf, s.stop)
	if cut >= 0 {
		s.emitText(id, buf[:cut])
		s.stopReason = "stop_sequence"
		s.stopSequence = seq
		s.finish("")
		return
	}
	keep := maxStopLen(s.stop) - 1
	if keep < 0 {
		keep = 0
	}
	if len(buf) <= keep {
		s.hold[id] = buf
		return
	}
	s.emitText(id, buf[:len(buf)-keep])
	s.hold[id] = buf[len(buf)-keep:]
}

func (s *anthropicStream) flushHold(id string) {
	if held := s.hold[id]; held != "" {
		s.emitText(id, held)
		delete(s.hold, id)
	}
}

func (s *anthropicStream) emitText(id, text string) {
	if text == "" {
		return
	}
	s.open(id, "text", map[string]any{"type": "text", "text": ""})
	s.blocks[id].streamed = true
	s.emitDelta(id, map[string]any{"type": "text_delta", "text": text})
}

func (s *anthropicStream) addThinking(id, delta string) {
	if delta == "" || s.finished {
		return
	}
	s.open(id, "thinking", map[string]any{"type": "thinking", "thinking": ""})
	s.blocks[id].streamed = true
	s.emitDelta(id, map[string]any{"type": "thinking_delta", "thinking": delta})
}

func (s *anthropicStream) addArgs(id, delta, call, name string) {
	if s.finished {
		return
	}
	s.sawTool = true
	block := map[string]any{"type": "tool_use", "id": call, "name": name, "input": map[string]any{}}
	s.open(id, "tool", block)
	if delta == "" {
		return
	}
	s.blocks[id].streamed = true
	s.emitDelta(id, map[string]any{"type": "input_json_delta", "partial_json": delta})
}

func (s *anthropicStream) stashEncrypted(id, encrypted string) {
	if encrypted == "" {
		return
	}
	if block := s.blocks[id]; block != nil && block.kind == "thinking" {
		block.encrypted = encrypted
		return
	}
	var found *contentBlock
	for _, block := range s.blocks {
		if block.kind != "thinking" || block.encrypted != "" {
			continue
		}
		if found != nil {
			return
		}
		found = block
	}
	if found != nil {
		found.encrypted = encrypted
	}
}

// sealThinking closes reasoning blocks before a later block starts. The
// signature has to precede content_block_stop, and the ciphertext often
// arrives on the item's done event rather than with the summary deltas.
func (s *anthropicStream) sealThinking() {
	for _, id := range s.order {
		block := s.blocks[id]
		if block == nil || block.kind != "thinking" || block.closed {
			continue
		}
		s.sign(id)
		s.close(id)
	}
}

func (s *anthropicStream) sign(id string) {
	block := s.blocks[id]
	if block == nil || block.kind != "thinking" || block.signed || block.closed {
		return
	}
	sig := "graft.nosig.v1"
	if block.encrypted != "" {
		sig = encodeSig(block.encrypted)
	}
	s.emitDelta(id, map[string]any{"type": "signature_delta", "signature": sig})
	block.signed = true
}

func (s *anthropicStream) open(id, kind string, content map[string]any) {
	if id == "" || s.finished {
		return
	}
	if block := s.blocks[id]; block != nil {
		return
	}
	if kind != "thinking" {
		s.sealThinking()
	}
	s.ensureStart()
	block := &contentBlock{index: len(s.order), kind: kind}
	s.blocks[id] = block
	s.order = append(s.order, id)
	s.emit("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         block.index,
		"content_block": content,
	})
}

func (s *anthropicStream) close(id string) {
	block := s.blocks[id]
	if block == nil || block.closed {
		return
	}
	block.closed = true
	s.emit("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": block.index,
	})
}

func (s *anthropicStream) emitDelta(id string, delta map[string]any) {
	block := s.blocks[id]
	if block == nil {
		return
	}
	s.emit("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": block.index,
		"delta": delta,
	})
}

func (s *anthropicStream) ensureStart() {
	if s.started {
		return
	}
	s.started = true
	id := s.id
	if id == "" {
		id = "msg_graft"
	}
	s.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         s.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": s.inputTokens, "output_tokens": 0},
		},
	})
}

func (s *anthropicStream) finish(reason string) {
	if s.finished || s.finishing {
		return
	}
	s.finishing = true
	for id, held := range s.hold {
		if held != "" && s.stopReason != "stop_sequence" {
			s.emitText(id, held)
		}
	}
	s.sealThinking()
	for _, id := range s.order {
		s.close(id)
	}
	s.finished = true
	if !s.started {
		s.ensureStart()
	}
	if reason != "" {
		s.stopReason = reason
	}
	if s.stopReason == "" {
		if s.sawTool {
			s.stopReason = "tool_use"
		} else {
			s.stopReason = "end_turn"
		}
	}
	s.emit("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   s.stopReason,
			"stop_sequence": s.stopSequence,
		},
		"usage": map[string]any{"input_tokens": s.inputTokens, "output_tokens": s.outputTokens},
	})
	s.emit("message_stop", map[string]any{"type": "message_stop"})
}

func (s *anthropicStream) fail(message string) {
	if message == "" {
		message = "upstream response failed"
	}
	if !s.started {
		s.ensureStart()
	}
	s.emit("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"message": message,
		},
	})
	s.finished = true
}

func (s *anthropicStream) noteResponse(raw any) {
	resp, _ := raw.(map[string]any)
	if resp == nil {
		return
	}
	if id := str(resp["id"]); id != "" && s.id == "" {
		s.id = id
	}
	if usage, ok := resp["usage"].(map[string]any); ok {
		if n := intOf(usage["input_tokens"]); n > 0 {
			s.inputTokens = n
		}
		if n := intOf(usage["output_tokens"]); n > 0 {
			s.outputTokens = n
		}
	}
}

func (s *anthropicStream) emit(event string, payload any) {
	body, err := marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.dst, "event: %s\ndata: %s\n\n", event, body)
}

func itemKey(payload map[string]any) string {
	if id := str(payload["item_id"]); id != "" {
		return id
	}
	if item, ok := payload["item"].(map[string]any); ok {
		if id := str(item["id"]); id != "" {
			return id
		}
	}
	if n, ok := number(payload["output_index"]); ok {
		return fmt.Sprintf("out-%d", int(n))
	}
	return ""
}

func earliestStop(buf string, stops []string) (int, string) {
	cut := -1
	seq := ""
	for _, stop := range stops {
		if stop == "" {
			continue
		}
		i := strings.Index(buf, stop)
		if i >= 0 && (cut < 0 || i < cut) {
			cut = i
			seq = stop
		}
	}
	return cut, seq
}

func maxStopLen(stops []string) int {
	n := 0
	for _, stop := range stops {
		if len(stop) > n {
			n = len(stop)
		}
	}
	return n
}

func errorMessage(payload map[string]any) string {
	if resp, ok := payload["response"].(map[string]any); ok {
		if errObj, ok := resp["error"].(map[string]any); ok {
			if msg := str(errObj["message"]); msg != "" {
				return msg
			}
		}
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg := str(errObj["message"]); msg != "" {
			return msg
		}
	}
	if msg := str(payload["message"]); msg != "" {
		return msg
	}
	return ""
}

func readSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var event string
	var data []string
	flush := func() error {
		if event == "" && len(data) == 0 {
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event = ""
		data = nil
		return err
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
