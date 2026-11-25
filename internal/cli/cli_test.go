package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/version"
)

func TestVersionCommand(t *testing.T) {
	old := version.Version
	version.Version = "v0.1.5"
	t.Cleanup(func() { version.Version = old })
	for _, arg := range []string{"version", "--version", "-v"} {
		out := captureStdout(t, func() int { return Main([]string{arg}) })
		if strings.TrimSpace(out) != "v0.1.5" {
			t.Fatalf("%s printed %q", arg, out)
		}
	}
}

func TestHelpIncludesVersion(t *testing.T) {
	old := version.Version
	version.Version = "v0.1.5"
	t.Cleanup(func() { version.Version = old })
	out := captureStdout(t, func() int { return Main(nil) })
	if !strings.Contains(out, "graft v0.1.5") || !strings.Contains(out, "graft version") || !strings.Contains(out, "graft claude") || !strings.Contains(out, "provider/model") {
		t.Fatalf("help = %q", out)
	}
}

func TestModelsCommandPrintsNamespaces(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := os.WriteFile(path, []byte(config.DefaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return Main([]string{"--config", path, "models"}) })
	if !strings.Contains(out, "volcengine-coding/deepseek-v4.1-flash") || !strings.Contains(out, "grok/grok-4.7-build-fast") {
		t.Fatalf("models = %q", out)
	}
}

func TestModelsCommandAlignsColumns(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := os.WriteFile(path, []byte(config.DefaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return Main([]string{"--config", path, "models"}) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("models output is too short: %q", out)
	}
	transportColumn := -1
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("model row is malformed: %q", line)
		}
		transport := fields[len(fields)-1]
		column := strings.LastIndex(line, transport)
		if transportColumn == -1 {
			transportColumn = column
		} else if column != transportColumn {
			t.Fatalf("transport column %d does not match %d: %q", column, transportColumn, line)
		}
	}
}

func TestModelsCommandGivesColumnsBreathingRoom(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := os.WriteFile(path, []byte(config.DefaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return Main([]string{"--config", path, "models"}) })
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("model row is malformed: %q", line)
		}
		transport := fields[len(fields)-1]
		column := strings.LastIndex(line, transport)
		gap := 0
		for i := column - 1; i >= 0 && line[i] == ' '; i-- {
			gap++
		}
		if gap < 4 {
			t.Fatalf("transport gap is %d, want at least 4: %q", gap, line)
		}
	}
}

func TestChooseModelReturnsProviderNamespace(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ref, err := chooseModel(cfg, "", bytes.NewBufferString("6\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "volcengine-coding/deepseek-v4.1-flash" {
		t.Fatalf("choice = %q", ref)
	}
	if !strings.Contains(out.String(), "volcengine-coding/deepseek-v4.1-flash") || !strings.Contains(out.String(), "DeepSeek V4.1 Flash") {
		t.Fatalf("prompt = %q", out.String())
	}
}

func TestChooseModelFiltersByProvider(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ref, err := chooseModel(cfg, "volcengine-coding", bytes.NewBufferString("2\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "volcengine-coding/deepseek-v4.1-flash-economy" {
		t.Fatalf("choice = %q", ref)
	}
	if strings.Contains(out.String(), "deepseek/deepseek-flash") {
		t.Fatalf("provider filter leaked another provider: %q", out.String())
	}
}

func TestChooseModelCanCancel(t *testing.T) {
	cfg, err := config.Parse("default", []byte(config.DefaultConfig))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := chooseModel(cfg, "", bytes.NewBufferString("q\n"), &out); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestModelPickerMovesWithArrowKeys(t *testing.T) {
	options := []pickerOption{
		{provider: "alpha", ref: "alpha/one", label: "One"},
		{provider: "beta", ref: "beta/two", label: "Two"},
		{provider: "gamma", ref: "gamma/three", label: "Three"},
	}
	var out bytes.Buffer
	ref, err := runModelPicker(options, bytes.NewBufferString("\x1b[B\x1b[B\r"), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "gamma/three" {
		t.Fatalf("choice = %q", ref)
	}
}

func TestModelPickerWrapsWithUpArrow(t *testing.T) {
	options := []pickerOption{
		{provider: "alpha", ref: "alpha/one", label: "One"},
		{provider: "beta", ref: "beta/two", label: "Two"},
	}
	var out bytes.Buffer
	ref, err := runModelPicker(options, bytes.NewBufferString("\x1b[A\r"), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "beta/two" {
		t.Fatalf("choice = %q", ref)
	}
}

func TestModelPickerUsesColor(t *testing.T) {
	options := []pickerOption{
		{provider: "alpha", ref: "alpha/one", label: "One"},
	}
	var out bytes.Buffer
	if _, err := runModelPicker(options, bytes.NewBufferString("\r"), &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("picker output has no ANSI color: %q", out.String())
	}
}

func TestModelPickerRowsFitEightyColumns(t *testing.T) {
	option := pickerOption{
		provider: "volcengine-coding",
		ref:      "volcengine-coding/deepseek-v4.1-flash-economy",
		label:    "DeepSeek V4.1 Flash Economy",
	}
	for _, selected := range []bool{false, true} {
		line := modelPickerRow(option, selected, true, 80)
		if width := utf8.RuneCountInString(stripSGR(line)); width > 80 {
			t.Fatalf("selected=%t: row is %d columns: %q", selected, width, line)
		}
	}
}

func TestModelPickerProviderHeadingIsNotRedundant(t *testing.T) {
	option := pickerOption{
		provider:      "volcengine-coding",
		providerLabel: "Volcengine Coding",
		ref:           "volcengine-coding/deepseek-v4.1-flash",
		label:         "DeepSeek V4.1 Flash",
	}
	var out bytes.Buffer
	renderModelPicker(&out, []pickerOption{option}, 0, false)
	lines := strings.Split(out.String(), "\r\n")
	if len(lines) < 5 {
		t.Fatalf("picker output is too short: %q", out.String())
	}
	if heading := strings.TrimSpace(lines[3]); heading != "Volcengine Coding" {
		t.Fatalf("provider heading = %q", heading)
	}
}

func TestModelPickerUsesCarriageReturnsWhileRaw(t *testing.T) {
	options := []pickerOption{
		{provider: "alpha", ref: "alpha/one", label: "One"},
		{provider: "beta", ref: "beta/two", label: "Two"},
	}
	var out bytes.Buffer
	if _, err := runModelPicker(options, bytes.NewBufferString("\r"), &out, false); err != nil {
		t.Fatal(err)
	}
	for i, b := range []byte(out.String()) {
		if b == '\n' && (i == 0 || out.Bytes()[i-1] != '\r') {
			t.Fatalf("bare LF at byte %d: %q", i, out.String())
		}
	}
}

func stripSGR(s string) string {
	for {
		start := strings.Index(s, "\x1b[")
		if start < 0 {
			return s
		}
		rest := s[start+2:]
		end := strings.IndexByte(rest, 'm')
		if end < 0 {
			return s
		}
		s = s[:start] + rest[end+1:]
	}
}

func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d, output %q", code, buf.String())
	}
	return buf.String()
}
