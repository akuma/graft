// Package config loads ~/.config/graft/config.toml.
//
// Providers are declared in that file. Adding a direct upstream is a config
// edit plus `graft sync`; no code change.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// DefaultConfig is written the first time graft runs with no config file.
const DefaultConfig = `[general]
# codex home; profiles are written next to the user config.toml
codex_home = "~/.codex"
# thin proxies listen on proxy_port + provider index, in file order
proxy_port = 8787
# base codex config is never modified; profiles layer on top of it
profile_prefix = "graft-"

[providers.grok]
label = "Grok Build"
# thin | direct
transport = "thin"
upstream = "https://cli-chat-proxy.grok.com/v1"
# borrow | api-key. borrow reads a CLI subscription token per request
auth = "borrow"
auth_source = "grok-cli"
# grok-4.7 and grok-4.6 publish a 500k window
context_window = 500000
# grok's proxy rejects codex multi-agent namespaces and strict web_search args
disable_agent_tools = true
disable_web_search = true

[[providers.grok.models]]
id = "grok-4.7"
display_name = "Grok 4.7"
input = ["text", "image"]
reasoning = ["low", "medium", "high", "xhigh"]

[[providers.grok.models]]
id = "grok-4.6"
display_name = "Grok 4.6"
input = ["text", "image"]
reasoning = ["low", "medium", "high", "xhigh"]

[providers.deepseek]
label = "DeepSeek"
transport = "direct"
upstream = "https://api.deepseek.com/"
# Claude Code appends /v1/messages. No trailing /v1.
anthropic_base = "https://api.deepseek.com/anthropic"
auth = "api-key"
# graft reads $DEEPSEEK_API_KEY, else ~/.config/graft/secrets.toml [keys].deepseek
key_env = "DEEPSEEK_API_KEY"
secrets_key = "deepseek"
context_window = 1048576
disable_agent_tools = false
disable_web_search = false
# Codex catalog fields. Omitted keys stay at the text-only, no-patch defaults.
shell_type = "shell_command"
apply_patch_tool_type = "freeform"
supports_search_tool = true
multi_agent_version = "v2"
supports_reasoning_summaries = true

[[providers.deepseek.models]]
id = "deepseek-flash"
display_name = "DeepSeek Flash"
input = ["text", "image"]
reasoning = ["low", "high", "max"]

[[providers.deepseek.models]]
id = "deepseek-v4-pro"
display_name = "DeepSeek V4 Pro"
input = ["text"]
reasoning = ["low", "high", "max"]

[providers.volcengine-coding]
label = "Volcengine Coding"
transport = "direct"
upstream = "https://ark.cn-beijing.volces.com/api/coding/v3"
anthropic_base = "https://ark.cn-beijing.volces.com/api/coding"
auth = "api-key"
# graft reads $ARK_API_KEY, else ~/.config/graft/secrets.toml [keys].volcengine-coding
key_env = "ARK_API_KEY"
secrets_key = "volcengine-coding"
context_window = 1048576
disable_agent_tools = false
disable_web_search = false
shell_type = "shell_command"
apply_patch_tool_type = "freeform"
supports_search_tool = true
multi_agent_version = "v2"
supports_reasoning_summaries = true

[[providers.volcengine-coding.models]]
id = "deepseek-v4.1-flash"
display_name = "DeepSeek V4.1 Flash"
input = ["text", "image"]
reasoning = ["low", "high", "max"]

[[providers.volcengine-coding.models]]
id = "deepseek-v4.1-flash-economy"
display_name = "DeepSeek V4.1 Flash Economy"
input = ["text", "image"]
reasoning = ["low", "high", "max"]
`

// Model is one catalog entry. Input and reasoning come from config; a bare
// id means text input and the default low/medium/high efforts.
type Model struct {
	Slug        string
	DisplayName string
	Input       []string
	Reasoning   []string
}

// Provider is one upstream Codex can be pointed at.
type Provider struct {
	Name              string
	Label             string
	Transport         string
	Upstream          string
	Auth              string
	Models            []Model
	EnvKey            string
	ContextWindow     int
	AuthSource        string
	KeyEnv            string
	SecretsKey        string
	DisableAgentTools bool
	DisableWebSearch  bool
	Port              int
	// AnthropicBase is the Messages API root for Claude Code, with no /v1 suffix.
	// Empty means this provider has no native Messages endpoint.
	AnthropicBase string
	Extra         map[string]any
}

// Slugs is the model ids in file order.
func (p Provider) Slugs() []string {
	out := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		if m.Slug != "" {
			out = append(out, m.Slug)
		}
	}
	return out
}

// FindModel returns the configured model, or a zero model with that slug.
func (p Provider) FindModel(slug string) Model {
	for _, m := range p.Models {
		if m.Slug == slug {
			return m
		}
	}
	return Model{Slug: slug}
}

// IsThin reports whether requests must be rewritten before they hit Upstream.
func (p Provider) IsThin() bool { return p.Transport == "thin" }

// ProfileName is the Codex --profile argument.
func (p Provider) ProfileName(prefix string) string { return prefix + p.Name }

// EnvVar is the variable Codex reads its bearer token from. graft fills it.
func (p Provider) EnvVar() string {
	if p.EnvKey != "" {
		return p.EnvKey
	}
	return "GRAFT_" + strings.ReplaceAll(strings.ToUpper(p.Name), "-", "_") + "_KEY"
}

// Config is the loaded user configuration.
type Config struct {
	Path          string
	CodexHome     string
	ProxyPort     int
	ProfilePrefix string
	Providers     map[string]*Provider
	Order         []string
}

// ProviderNamed returns a provider or an error listing the known names.
func (c *Config) ProviderNamed(name string) (*Provider, error) {
	p, ok := c.Providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(c.Order, ", "))
	}
	return p, nil
}

// ResolveModel picks a provider from a model reference, or a provider's default
// model. References use "provider/model"; the older "provider:model" form is
// accepted when the prefix names a configured provider.
func (c *Config) ResolveModel(model, provider string) (*Provider, string, error) {
	if model != "" {
		modelProvider, slug := c.splitModelRef(model)
		if modelProvider != "" {
			if provider != "" && provider != modelProvider {
				return nil, "", fmt.Errorf("model namespace %q conflicts with --provider %q", modelProvider, provider)
			}
			provider = modelProvider
		}
		if slug == "" {
			return nil, "", fmt.Errorf("model reference %q has no model name", model)
		}
		if provider != "" {
			p, err := c.ProviderNamed(provider)
			if err != nil {
				return nil, "", err
			}
			for _, known := range p.Slugs() {
				if known == slug {
					return p, slug, nil
				}
			}
			return nil, "", fmt.Errorf("unknown model %q for provider %q (known: %s)",
				slug, provider, strings.Join(p.Slugs(), ", "))
		}
		var matches []*Provider
		var known []string
		for _, name := range c.Order {
			p := c.Providers[name]
			for _, slug := range p.Slugs() {
				known = append(known, name+"/"+slug)
				if slug == model {
					matches = append(matches, p)
				}
			}
		}
		if len(matches) == 1 {
			for _, slug := range matches[0].Slugs() {
				if slug == model {
					return matches[0], slug, nil
				}
			}
		}
		if len(matches) > 1 {
			options := make([]string, 0, len(matches))
			for _, p := range matches {
				options = append(options, p.Name+"/"+model)
			}
			return nil, "", fmt.Errorf("model %q is ambiguous; use one of: %s", model, strings.Join(options, ", "))
		}
		return nil, "", fmt.Errorf("unknown model %q (known: %s)", model, strings.Join(known, ", "))
	}
	if provider != "" {
		p, err := c.ProviderNamed(provider)
		if err != nil {
			return nil, "", err
		}
		slug := ""
		if slugs := p.Slugs(); len(slugs) > 0 {
			slug = slugs[0]
		}
		return p, slug, nil
	}
	return nil, "", fmt.Errorf("no model given: pass -m <model> or --provider <name>")
}

func (c *Config) splitModelRef(model string) (provider, slug string) {
	if i := strings.Index(model, "/"); i > 0 {
		return model[:i], model[i+1:]
	}
	if i := strings.LastIndex(model, ":"); i > 0 {
		if _, ok := c.Providers[model[:i]]; ok {
			return model[:i], model[i+1:]
		}
	}
	return "", model
}

// Home is the directory holding config.toml and secrets.toml.
func Home() string {
	if env := os.Getenv("GRAFT_CONFIG_HOME"); env != "" {
		return expand(env)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(expand(base), "graft")
}

func expand(path string) string {
	path = os.ExpandEnv(path)
	switch {
	case path == "~":
		return os.Getenv("HOME")
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(os.Getenv("HOME"), path[2:])
	}
	return path
}

var providerHeading = regexp.MustCompile(`(?m)^\[providers\.([^]\s]+)\]`)

// Load reads path, or $GRAFT_CONFIG, or the default config file.
// A missing default file is created.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("GRAFT_CONFIG")
	}
	if path == "" {
		path = filepath.Join(Home(), "config.toml")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(DefaultConfig), 0o644); err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse decodes TOML. Provider order follows table order in the file so proxy
// ports stay stable across runs.
func Parse(path string, data []byte) (*Config, error) {
	var raw struct {
		General   map[string]any            `toml:"general"`
		Providers map[string]map[string]any `toml:"providers"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	general := raw.General
	if general == nil {
		general = map[string]any{}
	}
	codexHome := expand(str(general["codex_home"], "~/.codex"))
	proxyPort := intval(general["proxy_port"], 8787)
	prefix := str(general["profile_prefix"], "graft-")

	order := providerOrder(string(data), raw.Providers)
	providers := map[string]*Provider{}
	for i, name := range order {
		spec := raw.Providers[name]
		p := &Provider{
			Name:              name,
			Label:             str(spec["label"], name),
			Transport:         str(spec["transport"], "direct"),
			Upstream:          str(spec["upstream"], ""),
			Auth:              str(spec["auth"], "api-key"),
			Models:            parseModels(spec["models"]),
			EnvKey:            str(spec["env_key"], ""),
			ContextWindow:     intval(spec["context_window"], 0),
			AuthSource:        str(spec["auth_source"], ""),
			KeyEnv:            str(spec["key_env"], ""),
			SecretsKey:        str(spec["secrets_key"], ""),
			DisableAgentTools: boolval(spec["disable_agent_tools"]),
			DisableWebSearch:  boolval(spec["disable_web_search"]),
			Port:              intval(spec["port"], proxyPort+i),
			AnthropicBase:     strings.TrimRight(str(spec["anthropic_base"], ""), "/"),
			Extra:             extraKeys(spec),
		}
		providers[name] = p
	}
	return &Config{
		Path:          path,
		CodexHome:     codexHome,
		ProxyPort:     proxyPort,
		ProfilePrefix: prefix,
		Providers:     providers,
		Order:         order,
	}, nil
}

func providerOrder(text string, providers map[string]map[string]any) []string {
	var order []string
	seen := map[string]bool{}
	for _, m := range providerHeading.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if _, ok := providers[name]; ok && !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
	}
	for name := range providers {
		if !seen[name] {
			order = append(order, name)
		}
	}
	return order
}

func extraKeys(spec map[string]any) map[string]any {
	known := map[string]bool{
		"label": true, "transport": true, "upstream": true, "auth": true,
		"models": true, "env_key": true, "context_window": true, "auth_source": true,
		"key_env": true, "secrets_key": true, "disable_agent_tools": true,
		"disable_web_search": true, "port": true, "anthropic_base": true,
	}
	extra := map[string]any{}
	for k, v := range spec {
		if !known[k] {
			extra[k] = v
		}
	}
	return extra
}

func str(v any, fallback string) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}

func intval(v any, fallback int) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return fallback
	}
}

func boolval(v any) bool {
	b, _ := v.(bool)
	return b
}

func parseModels(v any) []Model {
	switch items := v.(type) {
	case []Model:
		return items
	case []string:
		out := make([]Model, len(items))
		for i, slug := range items {
			out[i] = Model{Slug: slug}
		}
		return out
	case []map[string]any:
		out := make([]Model, 0, len(items))
		for _, item := range items {
			if m, ok := modelFromMap(item); ok {
				out = append(out, m)
			}
		}
		return out
	case []any:
		out := make([]Model, 0, len(items))
		for _, item := range items {
			switch m := item.(type) {
			case string:
				out = append(out, Model{Slug: m})
			case map[string]any:
				if model, ok := modelFromMap(m); ok {
					out = append(out, model)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func modelFromMap(m map[string]any) (Model, bool) {
	slug := str(m["id"], "")
	if slug == "" {
		slug = str(m["slug"], "")
	}
	if slug == "" {
		return Model{}, false
	}
	return Model{
		Slug:        slug,
		DisplayName: str(m["display_name"], ""),
		Input:       strSlice(m["input"]),
		Reasoning:   strSlice(m["reasoning"]),
	}, true
}

func strSlice(v any) []string {
	switch items := v.(type) {
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return items
	default:
		return nil
	}
}
