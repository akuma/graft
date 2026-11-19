// Package creds resolves provider credentials.
//
// An api-key comes from an environment variable or secrets.toml. A borrowed
// credential is read from another CLI's auth file and never written back, so
// that CLI keeps owning refresh.
package creds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akuma/graft/internal/config"
	toml "github.com/pelletier/go-toml/v2"
)

// Error is a credential problem safe to print.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func secretsPath() string { return filepath.Join(config.Home(), "secrets.toml") }

type secretsFile struct {
	Keys map[string]string `toml:"keys"`
}

func loadSecrets() (secretsFile, error) {
	path := secretsPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return secretsFile{}, nil
	}
	if err != nil {
		return secretsFile{}, err
	}
	var file secretsFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return secretsFile{}, &Error{Msg: fmt.Sprintf("%s is not valid TOML: %v", path, err)}
	}
	return file, nil
}

// Slots reports stored key names and their lengths, never the values.
func Slots() (map[string]int, error) {
	file, err := loadSecrets()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for name, value := range file.Keys {
		out[name] = len(value)
	}
	return out, nil
}

// SaveSecret stores an API key with mode 0600, preserving other slots.
func SaveSecret(name, value string) (string, error) {
	file, err := loadSecrets()
	if err != nil {
		return "", err
	}
	if file.Keys == nil {
		file.Keys = map[string]string{}
	}
	file.Keys[name] = value
	var b strings.Builder
	b.WriteString("# graft secrets, keep this file private\n[keys]\n")
	for k, v := range file.Keys {
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s = %s\n", k, encoded)
	}
	path := secretsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// APIKey resolves a long-lived key. The environment variable wins over the file.
func APIKey(p *config.Provider) (string, error) {
	if p.KeyEnv != "" {
		if v := os.Getenv(p.KeyEnv); v != "" {
			return v, nil
		}
	}
	file, err := loadSecrets()
	if err != nil {
		return "", err
	}
	slot := p.SecretsKey
	if slot == "" {
		slot = p.Name
	}
	if file.Keys != nil && file.Keys[slot] != "" {
		return file.Keys[slot], nil
	}
	envName := p.KeyEnv
	if envName == "" {
		envName = "an env var"
	}
	return "", &Error{Msg: fmt.Sprintf("no api key for provider %q. Set %s or run: graft key set %s\nlooked at: %s, %s:[keys].%s",
		p.Name, envName, slot, envName, secretsPath(), slot)}
}

// Session is a borrowed CLI subscription token.
type Session struct {
	Key       string
	Email     string
	ExpiresAt time.Time
	Expired   bool
}

// GrokSession reads the freshest non-expired token from ~/.grok/auth.json.
func GrokSession() (Session, error) {
	store, err := readJSON(filepath.Join(os.Getenv("HOME"), ".grok", "auth.json"), "run: grok login")
	if err != nil {
		return Session{}, err
	}
	var best map[string]any
	var bestExp time.Time
	var bestLive bool
	for _, raw := range store {
		entry, ok := raw.(map[string]any)
		if !ok || str(entry["key"]) == "" {
			continue
		}
		exp := parseExpiry(entry["expires_at"])
		live := !exp.IsZero() && exp.After(time.Now())
		if best == nil || (live && !bestLive) || (live == bestLive && exp.After(bestExp)) {
			best, bestExp, bestLive = entry, exp, live
		}
	}
	if best == nil {
		return Session{}, &Error{Msg: "no subscription token in ~/.grok/auth.json; run: grok login"}
	}
	return Session{
		Key:       str(best["key"]),
		Email:     str(best["email"]),
		ExpiresAt: bestExp,
		Expired:   !bestExp.IsZero() && !bestExp.After(time.Now()),
	}, nil
}

// GrokClientVersion follows the installed Grok CLI so the proxy does not 426.
func GrokClientVersion() string {
	const fallback = "1.0.89"
	store, err := readJSON(filepath.Join(os.Getenv("HOME"), ".grok", "version.json"), "")
	if err != nil {
		return fallback
	}
	if v := str(store["version"]); v != "" {
		return v
	}
	return fallback
}

func readJSON(path, hint string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		msg := path + " not found"
		if hint != "" {
			msg += "; " + hint
		}
		return nil, &Error{Msg: msg}
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &Error{Msg: fmt.Sprintf("%s is not valid JSON: %v", path, err)}
	}
	return out, nil
}

func parseExpiry(v any) time.Time {
	switch n := v.(type) {
	case float64:
		return time.Unix(int64(n), 0)
	case string:
		t, err := time.Parse(time.RFC3339Nano, n)
		if err != nil {
			return time.Time{}
		}
		return t
	default:
		return time.Time{}
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// Check returns non-fatal notes for doctor.
func Check(p *config.Provider) []string {
	switch p.Auth {
	case "borrow":
		if p.AuthSource != "grok-cli" {
			return []string{fmt.Sprintf("unknown auth_source %q", p.AuthSource)}
		}
		session, err := GrokSession()
		if err != nil {
			return []string{err.Error()}
		}
		if session.Expired {
			email := session.Email
			if email == "" {
				email = "unknown"
			}
			return []string{fmt.Sprintf("grok subscription token expired; run `grok login` (account: %s)", email)}
		}
		return nil
	case "api-key":
		if _, err := APIKey(p); err != nil {
			return []string{err.Error()}
		}
		return nil
	default:
		return []string{fmt.Sprintf("unknown auth %q", p.Auth)}
	}
}
