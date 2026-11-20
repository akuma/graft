package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/creds"
	"github.com/akuma/graft/internal/gateway"
)

func TestLiveClaudeProviders(t *testing.T) {
	if os.Getenv("GRAFT_LIVE") == "" {
		t.Skip("set GRAFT_LIVE=1 to call Grok and DeepSeek")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("grok", func(t *testing.T) {
		p := cfg.Providers["grok"]
		gw := httptest.NewServer(gateway.Handler(p))
		defer gw.Close()
		body := []byte(`{"model":"grok-4.7","max_tokens":128,"stream":true,"output_config":{"effort":"low"},"messages":[{"role":"user","content":"Reply with the single word pong."}]}`)
		text := postMessages(t, gw.URL+"/v1/messages", body, "")
		if !strings.Contains(strings.ToLower(text), "pong") {
			t.Fatalf("grok reply missed pong: %s", clip(text))
		}
	})
	t.Run("grok-tool", func(t *testing.T) {
		p := cfg.Providers["grok"]
		gw := httptest.NewServer(gateway.Handler(p))
		defer gw.Close()
		body := []byte(`{"model":"grok-4.7","max_tokens":256,"stream":true,"thinking":{"type":"disabled"},"tools":[{"name":"ping","description":"Return pong","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"tool","name":"ping"},"messages":[{"role":"user","content":"ping"}]}`)
		text := postMessages(t, gw.URL+"/v1/messages", body, "")
		if !strings.Contains(text, `"name":"ping"`) && !strings.Contains(text, "tool_use") {
			t.Fatalf("grok tool reply: %s", clip(text))
		}
	})
	t.Run("grok-replay", func(t *testing.T) {
		p := cfg.Providers["grok"]
		gw := httptest.NewServer(gateway.Handler(p))
		defer gw.Close()
		first := postMessages(t, gw.URL+"/v1/messages", []byte(`{"model":"grok-4.7","max_tokens":128,"stream":false,"output_config":{"effort":"low"},"messages":[{"role":"user","content":"Reply with the single word pong."}]}`), "")
		var msg map[string]any
		if err := json.Unmarshal([]byte(first), &msg); err != nil {
			t.Fatal(err)
		}
		content, _ := msg["content"].([]any)
		var signed bool
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			sig, _ := block["signature"].(string)
			if block["type"] == "thinking" && strings.HasPrefix(sig, "graft.ec.v1.") {
				signed = true
			}
		}
		if !signed {
			t.Fatal("first turn did not return a graft reasoning signature")
		}
		secondBody, err := json.Marshal(map[string]any{
			"model":         "grok-4.7",
			"max_tokens":    64,
			"stream":        false,
			"output_config": map[string]any{"effort": "low"},
			"messages": []any{
				map[string]any{"role": "user", "content": "Reply with the single word pong."},
				map[string]any{"role": "assistant", "content": content},
				map[string]any{"role": "user", "content": "Say pong again."},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		again := postMessages(t, gw.URL+"/v1/messages", secondBody, "")
		if !strings.Contains(strings.ToLower(again), "pong") {
			t.Fatalf("replay reply missed pong: %s", clip(again))
		}
	})
	t.Run("deepseek", func(t *testing.T) {
		p := cfg.Providers["deepseek"]
		key, err := creds.APIKey(p)
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"model":"deepseek-flash","max_tokens":32,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`)
		text := postMessages(t, p.AnthropicBase+"/v1/messages", body, key)
		if !strings.Contains(strings.ToLower(text), "pong") {
			t.Fatalf("deepseek reply missed pong: %s", clip(text))
		}
	})
	t.Run("deepseek-v4-pro", func(t *testing.T) {
		p := cfg.Providers["deepseek"]
		key, err := creds.APIKey(p)
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"model":"deepseek-v4-pro","max_tokens":32,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`)
		text := postMessages(t, p.AnthropicBase+"/v1/messages", body, key)
		if !strings.Contains(strings.ToLower(text), "pong") {
			t.Fatalf("v4-pro reply missed pong: %s", clip(text))
		}
	})
	t.Run("grok-4.6", func(t *testing.T) {
		p := cfg.Providers["grok"]
		gw := httptest.NewServer(gateway.Handler(p))
		defer gw.Close()
		body := []byte(`{"model":"grok-4.6","max_tokens":64,"stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"Reply with the single word pong."}]}`)
		text := postMessages(t, gw.URL+"/v1/messages", body, "")
		if !strings.Contains(strings.ToLower(text), "pong") {
			t.Fatalf("grok-4.6 reply missed pong: %s", clip(text))
		}
	})
}

func postMessages(t *testing.T, url string, body []byte, key string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", "graft")
	}
	client := &http.Client{Timeout: 150 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s -> %d %s", url, res.StatusCode, clip(string(raw)))
	}
	return string(raw)
}

func clip(s string) string {
	if len(s) > 800 {
		return s[:800]
	}
	return s
}
