// Command graft runs Codex against a non-OpenAI provider.
package main

import (
	"os"

	"github.com/akuma/graft/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
