package catalog

import (
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestGrokCatalogContainsOnlyItsModels(t *testing.T) {
	p := &config.Provider{Name: "grok", Label: "Grok Build", Models: []string{"grok-4.7", "grok-4.6"}, ContextWindow: 256000}
	for _, slug := range p.Models {
		entry := Entry(p, slug)
		if strings.HasPrefix(entry["slug"].(string), "gpt-") {
			t.Fatalf("gpt slug leaked: %v", entry["slug"])
		}
	}
	if Entry(p, "grok-4.7")["apply_patch_tool_type"] != nil {
		t.Fatal("grok should not advertise freeform apply_patch")
	}
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
