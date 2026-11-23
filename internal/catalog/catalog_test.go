package catalog

import (
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestGrokCatalogContainsOnlyItsModels(t *testing.T) {
	p := &config.Provider{
		Name: "grok", Label: "Grok Build", ContextWindow: 500000,
		Models: []config.Model{
			{Slug: "grok-4.7", DisplayName: "Grok 4.7", Reasoning: []string{"low", "medium", "high", "xhigh"}},
			{Slug: "grok-4.6"},
		},
	}
	for _, slug := range p.Slugs() {
		entry := Entry(p, slug)
		if strings.HasPrefix(entry["slug"].(string), "gpt-") {
			t.Fatalf("gpt slug leaked: %v", entry["slug"])
		}
	}
	entry := Entry(p, "grok-4.7")
	if entry["apply_patch_tool_type"] != nil {
		t.Fatal("grok should not advertise freeform apply_patch")
	}
	if entry["display_name"] != "Grok 4.7" {
		t.Fatalf("display_name = %v", entry["display_name"])
	}
	if entry["context_window"] != 500000 || entry["max_context_window"] != 500000 {
		t.Fatalf("context = %v / %v", entry["context_window"], entry["max_context_window"])
	}
	efforts := effortNames(entry["supported_reasoning_levels"].([]any))
	if strings.Join(efforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("grok efforts = %v", efforts)
	}
	bare := Entry(p, "grok-4.6")
	if strings.Join(effortNames(bare["supported_reasoning_levels"].([]any)), ",") != "low,medium,high" {
		t.Fatalf("default efforts = %v", effortNames(bare["supported_reasoning_levels"].([]any)))
	}
	if bare["display_name"] != "grok-4.6" {
		t.Fatalf("display_name = %v", bare["display_name"])
	}
}

func TestImageInputFollowsTheModel(t *testing.T) {
	p := &config.Provider{
		Name: "custom", Label: "Custom",
		Models: []config.Model{
			{Slug: "sees", Input: []string{"text", "image"}},
			{Slug: "blind", Input: []string{"text"}},
			{Slug: "omitted"},
		},
	}
	if got := strings.Join(stringsOf(Entry(p, "sees")["input_modalities"].([]any)), ","); got != "text,image" {
		t.Fatalf("sees modalities = %s", got)
	}
	if Entry(p, "sees")["supports_image_detail_original"] != false {
		t.Fatal("original detail was enabled")
	}
	if got := strings.Join(stringsOf(Entry(p, "blind")["input_modalities"].([]any)), ","); got != "text" {
		t.Fatalf("blind modalities = %s", got)
	}
	if got := strings.Join(stringsOf(Entry(p, "omitted")["input_modalities"].([]any)), ","); got != "text" {
		t.Fatalf("omitted modalities = %s", got)
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

func TestDefaultConfigBuildsTheCatalog(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	flash := Entry(cfg.Providers["deepseek"], "deepseek-flash")
	if strings.Join(stringsOf(flash["input_modalities"].([]any)), ",") != "text,image" {
		t.Fatalf("flash input = %v", flash["input_modalities"])
	}
	if strings.Join(effortNames(flash["supported_reasoning_levels"].([]any)), ",") != "low,high,max" {
		t.Fatalf("flash efforts = %v", flash["supported_reasoning_levels"])
	}
	if flash["apply_patch_tool_type"] != "freeform" || flash["shell_type"] != "shell_command" || flash["multi_agent_version"] != "v2" {
		t.Fatalf("flash flags = apply %v shell %v multi %v", flash["apply_patch_tool_type"], flash["shell_type"], flash["multi_agent_version"])
	}
	pro := Entry(cfg.Providers["deepseek"], "deepseek-v4-pro")
	if strings.Join(stringsOf(pro["input_modalities"].([]any)), ",") != "text" {
		t.Fatalf("pro input = %v", pro["input_modalities"])
	}
	grok := Entry(cfg.Providers["grok"], "grok-4.7")
	if grok["apply_patch_tool_type"] != nil || grok["shell_type"] != "unified_exec" {
		t.Fatalf("grok flags = apply %v shell %v", grok["apply_patch_tool_type"], grok["shell_type"])
	}
}

func TestVolcengineCodingCatalogCarriesItsCapabilities(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["volcengine-coding"]
	if p == nil {
		t.Fatal("volcengine-coding provider is missing")
	}
	entry := Entry(p, "deepseek-v4.1-flash")
	if entry["display_name"] != "DeepSeek V4.1 Flash" {
		t.Fatalf("display_name = %v", entry["display_name"])
	}
	if entry["context_window"] != 1048576 || entry["max_context_window"] != 1048576 {
		t.Fatalf("context = %v / %v", entry["context_window"], entry["max_context_window"])
	}
	if got := strings.Join(stringsOf(entry["input_modalities"].([]any)), ","); got != "text,image" {
		t.Fatalf("input = %s", got)
	}
	if got := strings.Join(effortNames(entry["supported_reasoning_levels"].([]any)), ","); got != "low,high,max" {
		t.Fatalf("efforts = %s", got)
	}
	if entry["apply_patch_tool_type"] != "freeform" || entry["shell_type"] != "shell_command" || entry["supports_search_tool"] != true || entry["multi_agent_version"] != "v2" {
		t.Fatalf("catalog flags = apply %v shell %v search %v multi %v",
			entry["apply_patch_tool_type"], entry["shell_type"], entry["supports_search_tool"], entry["multi_agent_version"])
	}
}

func TestInstructionsAreEmbedded(t *testing.T) {
	entry := Entry(&config.Provider{Name: "grok", Label: "Grok"}, "grok-4.7")
	text := entry["model_messages"].(map[string]any)["instructions_template"].(string)
	if !strings.HasPrefix(text, "You are Codex") || len(text) < 1000 {
		t.Fatalf("instructions len %d", len(text))
	}
}
