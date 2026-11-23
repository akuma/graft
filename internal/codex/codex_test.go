package codex

import (
	"strings"
	"testing"

	"github.com/akuma/graft/internal/config"
)

func TestVolcengineCodingProfileUsesResponsesEndpoint(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["volcengine-coding"]
	if p == nil {
		t.Fatal("volcengine-coding provider is missing")
	}
	body := ProfileBody(cfg, p)
	for _, want := range []string{
		`model = "deepseek-v4.1-flash"`,
		`base_url = "https://ark.cn-beijing.volces.com/api/coding/v3"`,
		`env_key = "GRAFT_VOLCENGINE_CODING_KEY"`,
		`wire_api = "responses"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("profile missing %q:\n%s", want, body)
		}
	}
}
