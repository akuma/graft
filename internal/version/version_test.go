package version

import "testing"

func TestStampWinsOverBuildInfo(t *testing.T) {
	old := Version
	Version = "v0.1.5"
	t.Cleanup(func() { Version = old })
	if got := String(); got != "v0.1.5" {
		t.Fatalf("String() = %q", got)
	}
}

func TestDevIsNotEmpty(t *testing.T) {
	old := Version
	Version = "dev"
	t.Cleanup(func() { Version = old })
	if got := String(); got == "" {
		t.Fatal("empty version")
	}
}
