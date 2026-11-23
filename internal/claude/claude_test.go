package claude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestEnvPinsEveryAlias(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	deepseek := cfg.Providers["deepseek"]
	env := Env(deepseek, "deepseek-flash", "https://api.deepseek.com/anthropic", "sk-test")
	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"ANTHROPIC_CUSTOM_MODEL_OPTION",
	} {
		if env[key] != "deepseek-flash[1m]" {
			t.Fatalf("%s = %q", key, env[key])
		}
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" || env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" || env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("routing = %#v", env)
	}
	if env["CLAUDE_CODE_EFFORT_LEVEL"] != "max" {
		t.Fatalf("effort = %s", env["CLAUDE_CODE_EFFORT_LEVEL"])
	}
	if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1048576" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "786432" {
		t.Fatalf("window = %s compact = %s", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}
	if _, ok := env["CLAUDE_CODE_DISABLE_1M_CONTEXT"]; ok {
		t.Fatal("deepseek keeps its 1M window")
	}
	if !strings.Contains(env["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES"], "max_effort") || strings.Contains(env["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES"], "xhigh") {
		t.Fatalf("caps = %s", env["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES"])
	}
	if _, ok := env["CLAUDE_CODE_ATTRIBUTION_HEADER"]; ok {
		t.Fatal("direct deepseek should keep the attribution block")
	}
	base, err := BaseURL(deepseek)
	if err != nil || base != "https://api.deepseek.com/anthropic" {
		t.Fatalf("base %q %v", base, err)
	}

	grok := cfg.Providers["grok"]
	genv := Env(grok, "grok-4.7", "http://127.0.0.1:8887", "graft")
	if genv["ANTHROPIC_MODEL"] != "grok-4.7" {
		t.Fatalf("grok model = %s", genv["ANTHROPIC_MODEL"])
	}
	if genv["ANTHROPIC_AUTH_TOKEN"] != "" || genv["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("grok client credential = %#v", genv)
	}
	if genv["CLAUDE_CODE_EFFORT_LEVEL"] != "high" || genv["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "500000" || genv["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "375000" {
		t.Fatalf("grok env effort %s window %s compact %s", genv["CLAUDE_CODE_EFFORT_LEVEL"], genv["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], genv["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}
	if genv["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "1" {
		t.Fatalf("grok 1m flag = %q", genv["CLAUDE_CODE_DISABLE_1M_CONTEXT"])
	}
	if genv["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Fatal("grok attribution was left on")
	}
	caps := genv["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES"]
	if !strings.Contains(caps, "xhigh_effort") || strings.Contains(caps, "max_effort") {
		t.Fatalf("grok caps = %s", caps)
	}
	gbase, err := BaseURL(grok)
	if err != nil || gbase != "http://127.0.0.1:8887" {
		t.Fatalf("grok base %q %v", gbase, err)
	}
}

func TestVolcengineCodingUsesItsNativeMessagesEndpoint(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["volcengine-coding"]
	if p == nil {
		t.Fatal("volcengine-coding provider is missing")
	}
	base, err := BaseURL(p)
	if err != nil || base != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("base %q: %v", base, err)
	}
	env := Env(p, "deepseek-v4.1-flash", base, "test-key")
	if env["ANTHROPIC_MODEL"] != "deepseek-v4.1-flash[1m]" || env["ANTHROPIC_AUTH_TOKEN"] != "test-key" {
		t.Fatalf("model/auth = %q / %q", env["ANTHROPIC_MODEL"], env["ANTHROPIC_AUTH_TOKEN"])
	}
	if env["CLAUDE_CODE_EFFORT_LEVEL"] != "max" {
		t.Fatalf("effort = %q", env["CLAUDE_CODE_EFFORT_LEVEL"])
	}
}

func TestWriteSettingsDoesNotTouchClaudeHome(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteSettings(dir, "deepseek", map[string]string{"ANTHROPIC_AUTH_TOKEN": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	var decoded struct {
		Env map[string]string `json:"env"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
		t.Fatalf("settings = %#v", decoded.Env)
	}
}
