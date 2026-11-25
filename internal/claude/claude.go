// Package claude launches Claude Code against a graft provider.
//
// The user's ~/.claude is not modified. A --settings file for this session
// overrides the env block of user and project settings, which otherwise wins
// over the shell. Skills, hooks, MCP, and session history stay where they are.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/akuma/graft/internal/bridge"
	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/creds"
	"github.com/akuma/graft/internal/gateway"
)

// ExitError is the status Claude Code returned.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("claude exited %d", e.Code) }

// BaseURL is the Anthropic root Claude Code should use. It has no /v1 suffix.
func BaseURL(p *config.Provider) (string, error) {
	if p.IsThin() {
		return fmt.Sprintf("http://127.0.0.1:%d", gateway.Port(p)), nil
	}
	if p.AnthropicBase != "" {
		return p.AnthropicBase, nil
	}
	return "", fmt.Errorf("provider %q has no anthropic_base; Claude Code speaks Messages and %q does not", p.Name, p.Upstream)
}

// Env is the Claude Code environment for one model.
// Opus, Sonnet, Haiku, Fable, and the subagent alias still pin to that model
// so a leftover family row cannot fall through to a provider's claude-*
// mapping. The /model list itself is replaced by SessionSettings. A 1M
// window gets the [1m] suffix Claude Code strips before the request. A
// smaller window sets CLAUDE_CODE_MAX_CONTEXT_TOKENS and
// CLAUDE_CODE_DISABLE_1M_CONTEXT. Without the disable flag, Claude Code
// still labels an unrecognized model as a 1M opt-in (the default row shows
// "<id>[1m]") even when the meter follows MAX_CONTEXT_TOKENS.
func Env(p *config.Provider, slug, baseURL, token string) map[string]string {
	model := p.FindModel(slug)
	pinned := pinnedSlug(p, slug)
	name := model.DisplayName
	if name == "" {
		name = slug
	}
	env := map[string]string{
		"ANTHROPIC_BASE_URL":             baseURL,
		"ANTHROPIC_API_KEY":              "",
		"ANTHROPIC_MODEL":                pinned,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   pinned,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": pinned,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  pinned,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  pinned,
		"CLAUDE_CODE_SUBAGENT_MODEL":     pinned,
		"ANTHROPIC_CUSTOM_MODEL_OPTION":  pinned,
	}
	// A non-empty client token makes Claude Code treat the session as API-key
	// auth and disable claude.ai connectors. The Grok gateway ignores that
	// credential and reads the Grok login itself, so the token stays empty and
	// a shell token cannot turn the warning back on. Direct providers still
	// authenticate with their own key.
	if p.IsThin() {
		env["ANTHROPIC_AUTH_TOKEN"] = ""
	} else {
		env["ANTHROPIC_AUTH_TOKEN"] = token
	}
	if p.ContextWindow > 0 {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(p.ContextWindow)
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.Itoa(p.ContextWindow * 3 / 4)
	}
	if p.ContextWindow > 0 && p.ContextWindow < 1_000_000 {
		env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] = "1"
	}
	if effort := bridge.PreferredEffort(model.Reasoning); effort != "" {
		env["CLAUDE_CODE_EFFORT_LEVEL"] = effort
	}
	if caps := capabilities(model.Reasoning); caps != "" {
		for _, key := range []string{
			"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES",
			"ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES",
			"ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES",
			"ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES",
		} {
			env[key] = caps
		}
	}
	for _, key := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
		"ANTHROPIC_CUSTOM_MODEL_OPTION_NAME",
	} {
		env[key] = name
	}
	env["ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION"] = p.Label + " via graft"
	// The translator folds system blocks into one instructions string, so the
	// attribution block would ride along. DeepSeek sees the original array.
	if p.IsThin() {
		env["CLAUDE_CODE_ATTRIBUTION_HEADER"] = "0"
	}
	return env
}

func capabilities(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	has := map[string]bool{}
	for _, level := range levels {
		has[level] = true
	}
	caps := []string{"effort"}
	if has["xhigh"] {
		caps = append(caps, "xhigh_effort")
	}
	if has["max"] {
		caps = append(caps, "max_effort")
	}
	caps = append(caps, "thinking", "interleaved_thinking")
	return strings.Join(caps, ",")
}

func pinnedSlug(p *config.Provider, slug string) string {
	if p.ContextWindow >= 1_000_000 {
		return slug + "[1m]"
	}
	return slug
}

// SessionSettings is the one-session Claude Code settings document.
// modelPicker.replaceBuiltInOptions hides Opus/Sonnet/Haiku/Fable so /model
// lists this provider's models. availableModels keeps a leftover family
// alias from leaving the provider. Aliases in env remain pinned to the
// model this session started on.
func SessionSettings(p *config.Provider, env map[string]string) map[string]any {
	doc := map[string]any{"env": env}
	if model := env["ANTHROPIC_MODEL"]; model != "" {
		doc["model"] = model
	}
	options := pickerOptions(p)
	if len(options) == 0 {
		return doc
	}
	ids := make([]string, 0, len(options))
	for _, row := range options {
		ids = append(ids, row["model"])
	}
	doc["availableModels"] = ids
	doc["modelPicker"] = map[string]any{
		"replaceBuiltInOptions": true,
		"options":               options,
	}
	return doc
}

func pickerOptions(p *config.Provider) []map[string]string {
	slugs := p.Slugs()
	out := make([]map[string]string, 0, len(slugs))
	for _, slug := range slugs {
		model := p.FindModel(slug)
		label := model.DisplayName
		if label == "" {
			label = slug
		}
		out = append(out, map[string]string{
			"model": pinnedSlug(p, slug),
			"label": label,
		})
	}
	return out
}

// WriteSettings writes the one-session settings file. It contains the
// credential, so the file is mode 600. ~/.claude/settings.json is not touched.
func WriteSettings(dir, provider string, doc map[string]any) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	path := filepath.Join(dir, provider+".settings.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Launch runs claude in the foreground. A thin provider's Messages gateway is
// started for this session and stopped when claude exits. An already running
// gateway is left alone.
func Launch(cfg *config.Config, p *config.Provider, model string, args []string) error {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found on PATH")
	}
	var started bool
	if p.IsThin() {
		started, err = gateway.Ensure(cfg, p)
		if err != nil {
			return err
		}
		if started {
			defer gateway.Stop(p)
		}
		for _, note := range creds.Check(p) {
			fmt.Fprintf(os.Stderr, "graft: %s\n", note)
		}
	}
	token, err := credential(p)
	if err != nil {
		return err
	}
	base, err := BaseURL(p)
	if err != nil {
		return err
	}
	env := Env(p, model, base, token)
	settings, err := WriteSettings(filepath.Join(config.Home(), "claude"), p.Name, SessionSettings(p, env))
	if err != nil {
		return err
	}
	argv := append([]string{"--settings", settings}, args...)
	cmd := exec.Command(bin, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = mergeEnv(os.Environ(), env)
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGTERM)
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return &ExitError{Code: exit.ExitCode()}
		}
		return err
	}
	return nil
}

func credential(p *config.Provider) (string, error) {
	if p.IsThin() {
		// The gateway reads the Grok CLI login itself. Claude keeps its own
		// claude.ai login; a placeholder token would disable connectors.
		if notes := creds.Check(p); len(notes) > 0 {
			return "", fmt.Errorf("%s", notes[0])
		}
		return "", nil
	}
	return creds.APIKey(p)
}

func mergeEnv(base []string, overlay map[string]string) []string {
	out := make([]string, 0, len(base)+len(overlay))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if _, ok := overlay[key]; ok {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overlay))
	for key := range overlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overlay[key])
	}
	return out
}
