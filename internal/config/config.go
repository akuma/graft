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
models = ["grok-4.7", "grok-4.6"]

[providers.deepseek]
label = "DeepSeek"
transport = "direct"
upstream = "https://api.deepseek.com/"
auth = "api-key"
# graft reads $DEEPSEEK_API_KEY, else ~/.config/graft/secrets.toml [keys].deepseek
key_env = "DEEPSEEK_API_KEY"
secrets_key = "deepseek"
context_window = 1048576
disable_agent_tools = false
disable_web_search = false
models = ["deepseek-flash", "deepseek-v4-pro"]
`

// Provider is one upstream Codex can be pointed at.
type Provider struct {
	Name              string
	Label             string
	Transport         string
	Upstream          string
	Auth              string
	Models            []string
	EnvKey            string
	ContextWindow     int
	AuthSource        string
	KeyEnv            string
	SecretsKey        string
	DisableAgentTools bool
	DisableWebSearch  bool
	Port              int
	Extra             map[string]any
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

// ResolveModel picks a provider from a model slug, or a provider's default model.
// A "name:model" prefix is stripped so `grok:grok-4.7` still matches.
func (c *Config) ResolveModel(model, provider string) (*Provider, string, error) {
	if model != "" {
		normalized := model
		if i := strings.LastIndex(model, ":"); i >= 0 {
			normalized = model[i+1:]
		}
		var known []string
		for _, name := range c.Order {
			p := c.Providers[name]
			known = append(known, p.Models...)
			for _, slug := range p.Models {
				if slug == normalized {
					return p, normalized, nil
				}
			}
		}
		return nil, "", fmt.Errorf("unknown model %q (known: %s)", model, strings.Join(known, ", "))
	}
	if provider != "" {
		p, err := c.ProviderNamed(provider)
		if err != nil {
			return nil, "", err
		}
		slug := ""
		if len(p.Models) > 0 {
			slug = p.Models[0]
		}
		return p, slug, nil
	}
	return nil, "", fmt.Errorf("no model given: pass -m <model> or --provider <name>")
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
			Models:            strSlice(spec["models"]),
			EnvKey:            str(spec["env_key"], ""),
			ContextWindow:     intval(spec["context_window"], 0),
			AuthSource:        str(spec["auth_source"], ""),
			KeyEnv:            str(spec["key_env"], ""),
			SecretsKey:        str(spec["secrets_key"], ""),
			DisableAgentTools: boolval(spec["disable_agent_tools"]),
			DisableWebSearch:  boolval(spec["disable_web_search"]),
			Port:              intval(spec["port"], proxyPort+i),
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
		"disable_web_search": true, "port": true,
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
