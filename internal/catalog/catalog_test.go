package catalog

import (
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestGrokCatalogContainsOnlyItsModels(t *testing.T) {
	p := &config.Provider{Name: "grok", Label: "Grok Build", Models: []string{"grok-4.7", "grok-4.6"}, ContextWindow: 500000}
	for _, slug := range p.Models {
		entry := Entry(p, slug)
		if strings.HasPrefix(entry["slug"].(string), "gpt-") {
			t.Fatalf("gpt slug leaked: %v", entry["slug"])
		}
	}
	entry := Entry(p, "grok-4.7")
	if entry["apply_patch_tool_type"] != nil {
		t.Fatal("grok should not advertise freeform apply_patch")
	}
	if entry["context_window"] != 500000 || entry["max_context_window"] != 500000 {
		t.Fatalf("context = %v / %v", entry["context_window"], entry["max_context_window"])
	}
	efforts := effortNames(entry["supported_reasoning_levels"].([]any))
	if strings.Join(efforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("grok efforts = %v", efforts)
	}
	deepseek := Entry(&config.Provider{Name: "deepseek", Label: "DeepSeek"}, "deepseek-v4-pro")
	if strings.Join(effortNames(deepseek["supported_reasoning_levels"].([]any)), ",") != "low,high,max" {
		t.Fatalf("deepseek efforts = %v", effortNames(deepseek["supported_reasoning_levels"].([]any)))
	}
	if deepseek["default_reasoning_level"] != "high" {
		t.Fatalf("deepseek default = %v", deepseek["default_reasoning_level"])
	}
}

func TestImageInputFollowsTheModel(t *testing.T) {
	grok := &config.Provider{Name: "grok", Label: "Grok Build"}
	for _, slug := range []string{"grok-4.7", "grok-4.6"} {
		entry := Entry(grok, slug)
		if got := strings.Join(stringsOf(entry["input_modalities"].([]any)), ","); got != "text,image" {
			t.Fatalf("%s modalities = %s", slug, got)
		}
		if entry["supports_image_detail_original"] != false {
			t.Fatalf("%s original detail = %v", slug, entry["supports_image_detail_original"])
		}
	}
	flash := Entry(&config.Provider{Name: "deepseek", Label: "DeepSeek"}, "deepseek-flash")
	if got := strings.Join(stringsOf(flash["input_modalities"].([]any)), ","); got != "text,image" {
		t.Fatalf("flash modalities = %s", got)
	}
	pro := Entry(&config.Provider{Name: "deepseek", Label: "DeepSeek"}, "deepseek-v4-pro")
	if got := strings.Join(stringsOf(pro["input_modalities"].([]any)), ","); got != "text" {
		t.Fatalf("v4-pro modalities = %s", got)
	}
}

func stringsOf(values []any) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = v.(string)
	}
	return out
}

func effortNames(levels []any) []string {
	names := make([]string, 0, len(levels))
	for _, raw := range levels {
		names = append(names, raw.(map[string]any)["effort"].(string))
	}
	return names
}

func TestStrayConfigKeysDoNotEnterTheCatalog(t *testing.T) {
	p := &config.Provider{
		Name:  "grok",
		Label: "Grok Build",
		Extra: map[string]any{"notes": "do not leak", "shell_type": "shell_command"},
	}
	entry := Entry(p, "grok-4.7")
	if _, ok := entry["notes"]; ok {
		t.Fatal("notes leaked into the catalog")
	}
	if entry["shell_type"] != "shell_command" {
		t.Fatalf("shell_type = %v", entry["shell_type"])
	}
}

func TestInstructionsAreEmbedded(t *testing.T) {
	entry := Entry(&config.Provider{Name: "grok", Label: "Grok"}, "grok-4.7")
	text := entry["model_messages"].(map[string]any)["instructions_template"].(string)
	if !strings.HasPrefix(text, "You are Codex") || len(text) < 1000 {
		t.Fatalf("instructions len %d", len(text))
	}
}
