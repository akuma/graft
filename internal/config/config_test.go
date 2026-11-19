package config

import (
	"strings"
	"testing"
)

func TestDefaultConfigCarriesModelFacts(t *testing.T) {
	cfg, err := Parse("default", []byte(DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	grok := cfg.Providers["grok"]
	if strings.Join(grok.Slugs(), ",") != "grok-4.7,grok-4.6" {
		t.Fatalf("grok models = %v", grok.Slugs())
	}
	flash := grok.FindModel("grok-4.7")
	if flash.DisplayName != "Grok 4.7" || strings.Join(flash.Input, ",") != "text,image" || strings.Join(flash.Reasoning, ",") != "low,medium,high,xhigh" {
		t.Fatalf("grok-4.7 = %+v", flash)
	}
	deepseek := cfg.Providers["deepseek"]
	pro := deepseek.FindModel("deepseek-v4-pro")
	if strings.Join(pro.Input, ",") != "text" || strings.Join(pro.Reasoning, ",") != "low,high,max" {
		t.Fatalf("v4-pro = %+v", pro)
	}
	see := deepseek.FindModel("deepseek-flash")
	if strings.Join(see.Input, ",") != "text,image" {
		t.Fatalf("flash input = %v", see.Input)
	}
	if deepseek.Extra["apply_patch_tool_type"] != "freeform" || deepseek.Extra["shell_type"] != "shell_command" || deepseek.Extra["supports_search_tool"] != true || deepseek.Extra["multi_agent_version"] != "v2" || deepseek.Extra["supports_reasoning_summaries"] != true {
		t.Fatalf("deepseek extra = %#v", deepseek.Extra)
	}
	if grok.Extra["apply_patch_tool_type"] != nil {
		t.Fatalf("grok extra = %#v", grok.Extra)
	}
}

func TestBareModelListStaysTextOnly(t *testing.T) {
	cfg, err := Parse("bare", []byte(`
[providers.kimi]
models = ["kimi-k2", "kimi-k3"]
`))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["kimi"]
	if strings.Join(p.Slugs(), ",") != "kimi-k2,kimi-k3" {
		t.Fatalf("slugs = %v", p.Slugs())
	}
	if len(p.FindModel("kimi-k2").Input) != 0 || len(p.FindModel("kimi-k2").Reasoning) != 0 {
		t.Fatalf("bare model = %+v", p.FindModel("kimi-k2"))
	}
}
