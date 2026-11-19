package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

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
	if !strings.Contains(out, "graft v0.1.5") || !strings.Contains(out, "graft version") {
		t.Fatalf("help = %q", out)
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
