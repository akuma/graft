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
	if deepseek.AnthropicBase != "https://api.deepseek.com/anthropic" {
		t.Fatalf("anthropic base = %q", deepseek.AnthropicBase)
	}
	if _, ok := deepseek.Extra["anthropic_base"]; ok {
		t.Fatal("anthropic_base leaked into the codex catalog extra")
	}
}

func TestDefaultConfigIncludesVolcengineCoding(t *testing.T) {
	cfg, err := Parse("default", []byte(DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["volcengine-coding"]
	if p == nil {
		t.Fatal("volcengine-coding provider is missing")
	}
	if p.Label != "Volcengine Coding" || p.Transport != "direct" {
		t.Fatalf("provider = %+v", p)
	}
	if p.Upstream != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("upstream = %q", p.Upstream)
	}
	if p.AnthropicBase != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("anthropic base = %q", p.AnthropicBase)
	}
	if p.KeyEnv != "ARK_API_KEY" || p.SecretsKey != "volcengine-coding" {
		t.Fatalf("credential config = env %q slot %q", p.KeyEnv, p.SecretsKey)
	}
	if p.ContextWindow != 1048576 {
		t.Fatalf("context window = %d", p.ContextWindow)
	}
	if strings.Join(p.Slugs(), ",") != "deepseek-v4.1-flash,deepseek-v4.1-flash-economy" {
		t.Fatalf("models = %v", p.Slugs())
	}
	for _, slug := range p.Slugs() {
		model := p.FindModel(slug)
		if strings.Join(model.Input, ",") != "text,image" || strings.Join(model.Reasoning, ",") != "low,high,max" {
			t.Fatalf("%s = %+v", slug, model)
		}
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

func TestResolveModelUsesProviderNamespace(t *testing.T) {
	cfg, err := Parse("default", []byte(DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	p, slug, err := cfg.ResolveModel("volcengine-coding/deepseek-v4.1-flash", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "volcengine-coding" || slug != "deepseek-v4.1-flash" {
		t.Fatalf("resolved %s/%s", p.Name, slug)
	}
	if _, _, err := cfg.ResolveModel("deepseek/deepseek-v4.1-flash", ""); err == nil {
		t.Fatal("namespace selected the wrong provider")
	}
}

func TestResolveModelRejectsAmbiguousBareSlug(t *testing.T) {
	cfg, err := Parse("ambiguous", []byte(`
[providers.alpha]
models = ["shared"]

[providers.beta]
models = ["shared"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.ResolveModel("shared", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error = %v", err)
	}
	p, slug, err := cfg.ResolveModel("shared", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "beta" || slug != "shared" {
		t.Fatalf("provider-scoped model = %s/%s", p.Name, slug)
	}
}
