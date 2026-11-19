// Package cli is the graft command line.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/akuma/graft/internal/codex"
	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/creds"
	"github.com/akuma/graft/internal/proxy"
)

// Main runs graft and returns a process exit code.
func Main(argv []string) int {
	if len(argv) > 0 && argv[0] == "codex" {
		return cmdCodex(argv[1:])
	}
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" {
		usage(os.Stdout)
		return 0
	}
	configPath, rest := takeConfig(argv)
	if len(rest) == 0 {
		usage(os.Stderr)
		return 2
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch rest[0] {
	case "init":
		fmt.Println("config: " + cfg.Path)
		return finish(codex.Sync(cfg, true))
	case "sync":
		quiet := len(rest) > 1 && (rest[1] == "-q" || rest[1] == "--quiet")
		return finish(codex.Sync(cfg, !quiet))
	case "models":
		for _, name := range cfg.Order {
			p := cfg.Providers[name]
			for _, model := range p.Slugs() {
				fmt.Printf("%-18s -> %s (%s)\n", model, p.Name, p.Transport)
			}
		}
		return 0
	case "status":
		return cmdStatus(cfg)
	case "doctor":
		return cmdDoctor(cfg)
	case "proxy":
		return cmdProxy(cfg, rest[1:])
	case "key":
		return cmdKey(rest[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", rest[0])
		usage(os.Stderr)
		return 2
	}
}

func cmdCodex(args []string) int {
	var model, provider, configPath string
	for len(args) > 0 {
		switch {
		case args[0] == "--config" && len(args) > 1:
			configPath, args = args[1], args[2:]
		case args[0] == "-m" || args[0] == "--model":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "-m needs a model")
				return 2
			}
			model, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--model="):
			model, args = strings.TrimPrefix(args[0], "--model="), args[1:]
		case args[0] == "--provider" && len(args) > 1:
			provider, args = args[1], args[2:]
		default:
			goto done
		}
	}
done:
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p, slug, err := cfg.ResolveModel(model, provider)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := codex.Launch(cfg, p, slug, args); err != nil {
		if exit, ok := err.(*codex.ExitError); ok {
			return exit.Code
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdStatus(cfg *config.Config) int {
	fmt.Println("config  " + cfg.Path)
	fmt.Println("codex   " + cfg.CodexHome)
	fmt.Println()
	for _, name := range cfg.Order {
		p := cfg.Providers[name]
		state := "-"
		if p.IsThin() {
			if proxy.Healthy(p) {
				state = "healthy"
			} else {
				state = "down"
			}
		}
		profile := "profile MISSING (run: graft sync)"
		if _, err := os.Stat(codex.ProfilePath(cfg, p)); err == nil {
			profile = "profile ok"
		}
		fmt.Printf("%-10s %-7s %-12s proxy=%s\n", p.Name, p.Transport, profile, state)
		fmt.Printf("%-10s models: %s\n", "", strings.Join(p.Slugs(), ", "))
		fmt.Printf("%-10s base_url: %s\n", "", p.Upstream)
	}
	return 0
}

func cmdDoctor(cfg *config.Config) int {
	problems := 0
	for _, name := range cfg.Order {
		p := cfg.Providers[name]
		fmt.Printf("[%s]\n", p.Name)
		for _, note := range creds.Check(p) {
			fmt.Println("  ! " + note)
			problems++
		}
		if p.Auth == "borrow" && p.AuthSource == "grok-cli" {
			session, err := creds.GrokSession()
			if err != nil {
				fmt.Println("  ! " + err.Error())
				problems++
			} else {
				expiry := "ok"
				if session.Expired {
					expiry = "expired"
				}
				email := session.Email
				if email == "" {
					email = "unknown"
				}
				fmt.Printf("  token %s for %s\n", expiry, email)
			}
		}
		if p.IsThin() {
			state := "down"
			if proxy.Healthy(p) {
				state = "healthy"
			}
			fmt.Printf("  proxy %s (port %d)\n", state, p.Port)
		}
	}
	if problems > 0 {
		return 1
	}
	return 0
}

func cmdProxy(cfg *config.Config, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: graft proxy start|stop|health|run [--provider name]")
		return 2
	}
	action := args[0]
	var name string
	for i := 1; i < len(args); i++ {
		if args[i] == "--provider" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	var list []*config.Provider
	if name != "" {
		p, err := cfg.ProviderNamed(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		list = []*config.Provider{p}
	} else {
		for _, n := range cfg.Order {
			list = append(list, cfg.Providers[n])
		}
	}
	if action == "run" {
		if name == "" {
			fmt.Fprintln(os.Stderr, "proxy run needs --provider")
			return 2
		}
		if err := proxy.Serve(list[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	for _, p := range list {
		if !p.IsThin() {
			fmt.Printf("%s: transport=direct, no proxy needed\n", p.Name)
			continue
		}
		switch action {
		case "start":
			if _, err := proxy.Ensure(cfg, p); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		case "stop":
			if proxy.Stop(p) {
				fmt.Printf("%s: stopped\n", p.Name)
			} else {
				fmt.Printf("%s: not running\n", p.Name)
			}
		case "health":
			state := "down"
			if proxy.Healthy(p) {
				state = "healthy"
			}
			fmt.Printf("%s: %s (port %d)\n", p.Name, state, p.Port)
		default:
			fmt.Fprintf(os.Stderr, "unknown proxy action %q\n", action)
			return 2
		}
	}
	return 0
}

func cmdKey(args []string) int {
	if len(args) == 0 || (args[0] != "set" && args[0] != "list") {
		fmt.Fprintln(os.Stderr, "usage: graft key set <slot> [value] | graft key list")
		return 2
	}
	if args[0] == "list" {
		slots, err := creds.Slots()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(slots) == 0 {
			fmt.Println("no keys stored")
			return 0
		}
		for name, n := range slots {
			fmt.Printf("%-12s stored (%d chars)\n", name, n)
		}
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: graft key set <slot> [value]")
		return 2
	}
	slot := args[1]
	value := ""
	if len(args) > 2 {
		value = args[2]
	} else {
		if isTTY() {
			fmt.Fprintf(os.Stderr, "paste key for %q then press enter: ", slot)
		}
		line, err := readLine()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		value = strings.TrimSpace(line)
	}
	if value == "" {
		fmt.Fprintln(os.Stderr, "empty key, nothing written")
		return 1
	}
	path, err := creds.SaveSecret(slot, value)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("stored %s in %s (mode 600)\n", slot, path)
	return 0
}

func readLine() (string, error) {
	buf := make([]byte, 0, 128)
	tmp := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(tmp)
		if n == 1 {
			if tmp[0] == '\n' {
				return string(buf), nil
			}
			buf = append(buf, tmp[0])
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return string(buf), nil
			}
			return "", err
		}
	}
}

func isTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func takeConfig(argv []string) (string, []string) {
	var path string
	rest := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--config" && i+1 < len(argv) {
			path = argv[i+1]
			i++
			continue
		}
		rest = append(rest, argv[i])
	}
	return path, rest
}

func finish(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `graft runs Codex against a non-OpenAI provider.

Usage:
  graft codex -m grok-4.7 [codex args...]
  graft codex -m deepseek-flash [codex args...]
  graft init
  graft sync [-q]
  graft models
  graft status
  graft doctor
  graft proxy start|stop|health [--provider name]
  graft key set <slot> [value]
  graft key list

--config <file> selects the config. -m, --provider and --config before the
codex arguments are graft's; everything after them is passed to codex.
`)
}
