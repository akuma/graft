// Package catalog writes the model list Codex's /model picker reads.
//
// Without a catalog Codex shows its bundled GPT models, which this provider
// cannot serve. model_catalog_json replaces that list, so each profile gets a
// catalog containing only its own models.
package catalog

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/akuma/graft/internal/config"
)

//go:embed codex_instructions.txt
var instructions string

// Codex requires instructions_template. The embedded text is the Codex prompt
// DeepSeek publishes for its own catalog, not one written for graft.

var baseEntry = map[string]any{
	"shell_type":                        "unified_exec",
	"visibility":                        "list",
	"supported_in_api":                  true,
	"priority":                          1,
	"include_skills_usage_instructions": false,
	"include_plugin_usage_instructions": false,
	"include_apps_usage_instructions":   false,
	"default_reasoning_summary":         "none",
	"support_verbosity":                 true,
	"default_verbosity":                 "low",
	"truncation_policy":                 map[string]any{"mode": "tokens", "limit": 10000},
	"comp_hash":                         "3000",
	"effective_context_window_percent":  95,
	"input_modalities":                  []any{"text"},
	"supports_image_detail_original":    false,
	"supports_experimental_context":     false,
	"prefer_websockets":                 false,
	"availability_nux":                  nil,
	"upgrade":                           nil,
	"auto_review_model_override":        nil,
	"experimental_supported_tools":      []any{},
	"supports_parallel_tool_calls":      true,
}

// conservative is the default capability set: no freeform apply_patch, no
// search tool, responses-lite off (it strips every tool).
var conservative = map[string]any{
	"apply_patch_tool_type":        nil,
	"web_search_tool_type":         "text",
	"supports_search_tool":         false,
	"use_responses_lite":           false,
	"tool_mode":                    nil,
	"multi_agent_version":          nil,
	"supports_reasoning_summaries": false,
	"reasoning_summary_format":     "experimental",
}

var effortDescription = map[string]string{
	"low":    "Fast responses with lighter reasoning",
	"medium": "Balanced reasoning for everyday work",
	"high":   "Greater reasoning depth for complex problems",
	"xhigh":  "Extra high reasoning depth for complex problems",
	"max":    "Maximum reasoning depth for complex problems",
}

// Path is where the provider's catalog is written.
func Path(cfg *config.Config, p *config.Provider) string {
	return filepath.Join(cfg.CodexHome, p.ProfileName(cfg.ProfilePrefix)+"-models.json")
}

// Entry builds one catalog record.
func Entry(p *config.Provider, slug string) map[string]any {
	entry := map[string]any{}
	for k, v := range baseEntry {
		entry[k] = v
	}
	for k, v := range conservative {
		entry[k] = v
	}
	overridable := map[string]bool{}
	for k := range baseEntry {
		overridable[k] = true
	}
	for k := range conservative {
		overridable[k] = true
	}
	for k, v := range p.Extra {
		if overridable[k] && (v != nil || k == "apply_patch_tool_type") {
			entry[k] = v
		}
	}
	model := p.FindModel(slug)
	name := model.DisplayName
	if name == "" {
		name = slug
	}
	entry["slug"] = slug
	entry["display_name"] = name
	entry["description"] = p.Label + " via graft"
	entry["default_reasoning_level"] = "high"
	entry["supported_reasoning_levels"] = reasoningLevels(model.Reasoning)
	text := instructions
	if text == "" {
		text = "You are Codex, an autonomous coding agent. Complete the user's request."
	}
	entry["model_messages"] = map[string]any{"instructions_template": text}
	if p.ContextWindow > 0 {
		entry["context_window"] = p.ContextWindow
		entry["max_context_window"] = p.ContextWindow
	}
	// Codex gates pasted images and view_image on input_modalities. The model
	// config lists them. An empty input stays text-only. detail "original"
	// stays off so Codex sends a resized JPEG or PNG.
	if len(model.Input) > 0 {
		entry["input_modalities"] = stringsToAny(model.Input)
	}
	return entry
}

func reasoningLevels(efforts []string) []any {
	if len(efforts) == 0 {
		efforts = []string{"low", "medium", "high"}
	}
	out := make([]any, len(efforts))
	for i, effort := range efforts {
		text := effortDescription[effort]
		if text == "" {
			text = effort
		}
		out[i] = map[string]any{"effort": effort, "description": text}
	}
	return out
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// Write writes the catalog if it changed. Providers with no models are skipped.
func Write(cfg *config.Config, p *config.Provider, verbose bool) (string, error) {
	if len(p.Models) == 0 {
		return "", nil
	}
	models := make([]any, 0, len(p.Models))
	for _, slug := range p.Slugs() {
		models = append(models, Entry(p, slug))
	}
	payload, err := json.MarshalIndent(map[string]any{"models": models}, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	path := Path(cfg, p)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(payload) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return "", err
	}
	if verbose {
		println("wrote " + path)
	}
	return path, nil
}
