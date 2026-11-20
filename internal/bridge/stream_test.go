package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateStreamTextToolAndReasoning(t *testing.T) {
	var sse strings.Builder
	write := func(event string, payload any) {
		body, err := marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		sse.WriteString("event: " + event + "\ndata: " + string(body) + "\n\n")
	}
	write("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
	write("response.output_item.added", map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "rs_1", "type": "reasoning"}})
	write("response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "delta": "hmm"})
	write("response.output_item.done", map[string]any{"type": "response.output_item.done", "item": map[string]any{
		"id": "rs_1", "type": "reasoning", "encrypted_content": "cipher",
		"summary": []any{map[string]any{"type": "summary_text", "text": "hmm"}},
	}})
	write("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": "pon"})
	write("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": "g"})
	write("response.output_item.added", map[string]any{"type": "response.output_item.added", "item": map[string]any{
		"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "Bash",
	}})
	write("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "delta": `{"cmd":"ls"}`})
	write("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_1", "status": "completed",
		"usage": map[string]any{"input_tokens": 5, "output_tokens": 6},
		"output": []any{
			map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "cipher"},
			map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "pong"}}},
			map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": `{"cmd":"ls"}`},
		},
	}})

	var out strings.Builder
	if err := TranslateStream(&out, strings.NewReader(sse.String()), "grok-4.7", nil); err != nil {
		t.Fatal(err)
	}
	events := parseEvents(t, out.String())
	if events[0].event != "message_start" {
		t.Fatalf("first = %s", events[0].event)
	}
	var kinds []string
	var signature string
	for _, ev := range events {
		switch ev.event {
		case "content_block_start":
			kinds = append(kinds, ev.data["content_block"].(map[string]any)["type"].(string))
		case "content_block_delta":
			delta := ev.data["delta"].(map[string]any)
			if delta["type"] == "signature_delta" {
				signature = delta["signature"].(string)
			}
		}
	}
	if strings.Join(kinds, ",") != "thinking,text,tool_use" {
		t.Fatalf("blocks = %s\n%s", strings.Join(kinds, ","), out.String())
	}
	plain, ok := decodeSig(signature)
	if !ok || plain != "cipher" {
		t.Fatalf("signature %q -> %q", signature, plain)
	}
	last := events[len(events)-1]
	delta := events[len(events)-2]
	if last.event != "message_stop" || delta.data["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("tail = %s %s", delta.event, last.event)
	}
	joined := out.String()
	if !strings.Contains(joined, `"text":"pon"`) || !strings.Contains(joined, `"text":"g"`) {
		t.Fatal("text deltas missing")
	}
}

func TestTranslateStreamStopSequence(t *testing.T) {
	body, _ := marshal(map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_1", "delta": "hello STOP world",
	})
	done, _ := marshal(map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "resp_9", "status": "completed", "output": []any{}},
	})
	src := "event: response.output_text.delta\ndata: " + string(body) + "\n\nevent: response.completed\ndata: " + string(done) + "\n\n"
	var out strings.Builder
	if err := TranslateStream(&out, strings.NewReader(src), "grok-4.7", []string{"STOP"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "world") {
		t.Fatalf("stop leaked: %s", out.String())
	}
	if !strings.Contains(out.String(), `"stop_reason":"stop_sequence"`) || !strings.Contains(out.String(), `"text":"hello "`) {
		t.Fatalf("stream = %s", out.String())
	}
}

func TestCompletedEventCarriesWholeOutput(t *testing.T) {
	body, _ := marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_3", "status": "completed", "model": "grok-4.7",
			"usage": map[string]any{"input_tokens": 2, "output_tokens": 1},
			"output": []any{map[string]any{
				"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "pong"}},
			}},
		},
	})
	var out strings.Builder
	if err := TranslateStream(&out, strings.NewReader("data: "+string(body)+"\n\n"), "grok-4.7", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"text":"pong"`) || !strings.Contains(out.String(), `"stop_reason":"end_turn"`) {
		t.Fatalf("stream = %s", out.String())
	}
}

type sseEvent struct {
	event string
	data  map[string]any
}

func parseEvents(t *testing.T, raw string) []sseEvent {
	t.Helper()
	var events []sseEvent
	var event, data string
	flush := func() {
		if data == "" {
			event = ""
			return
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, sseEvent{event: event, data: payload})
		event, data = "", ""
	}
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	flush()
	return events
}
