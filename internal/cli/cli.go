// Package cli is the graft command line.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/akuma/graft/internal/claude"
	"github.com/akuma/graft/internal/codex"
	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/creds"
	"github.com/akuma/graft/internal/gateway"
	"github.com/akuma/graft/internal/proxy"
	"github.com/akuma/graft/internal/version"
	"golang.org/x/term"
)

// Main runs graft and returns a process exit code.
func Main(argv []string) int {
	if len(argv) > 0 && (argv[0] == "codex" || argv[0] == "claude") {
		return cmdClient(argv[0], argv[1:])
	}
	if len(argv) == 1 && isVersion(argv[0]) {
		fmt.Println(version.String())
		return 0
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
		refWidth, labelWidth := 48, 16
		for _, name := range cfg.Order {
			p := cfg.Providers[name]
			if width := utf8.RuneCountInString(p.Label); width > labelWidth {
				labelWidth = width
			}
			for _, model := range p.Slugs() {
				if width := utf8.RuneCountInString(name + "/" + model); width > refWidth {
					refWidth = width
				}
			}
		}
		for _, name := range cfg.Order {
			p := cfg.Providers[name]
			for _, model := range p.Slugs() {
				fmt.Printf("%-*s  %-*s    %s\n", refWidth, name+"/"+model, labelWidth, p.Label, p.Transport)
			}
		}
		return 0
	case "status":
		return cmdStatus(cfg)
	case "doctor":
		return cmdDoctor(cfg)
	case "proxy":
		return cmdProxy(cfg, rest[1:])
	case "gateway":
		return cmdGateway(cfg, rest[1:])
	case "key":
		return cmdKey(rest[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", rest[0])
		usage(os.Stderr)
		return 2
	}
}

func cmdClient(kind string, args []string) int {
	model, provider, configPath, rest, code := takeLaunchFlags(args)
	if code >= 0 {
		return code
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if model == "" && isTTY() {
		var selectErr error
		model, selectErr = chooseModel(cfg, provider, os.Stdin, os.Stderr)
		if selectErr != nil {
			fmt.Fprintln(os.Stderr, selectErr)
			return 2
		}
	}
	p, slug, err := cfg.ResolveModel(model, provider)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var launchErr error
	if kind == "claude" {
		launchErr = claude.Launch(cfg, p, slug, rest)
	} else {
		launchErr = codex.Launch(cfg, p, slug, rest)
	}
	if launchErr != nil {
		switch exit := launchErr.(type) {
		case *codex.ExitError:
			return exit.Code
		case *claude.ExitError:
			return exit.Code
		}
		fmt.Fprintln(os.Stderr, launchErr)
		return 1
	}
	return 0
}

// takeLaunchFlags reads graft's own flags. The first flag it does not own
// stops the scan, so a later client flag such as --yolo is passed through.
// code is -1 when parsing should continue.
func takeLaunchFlags(args []string) (model, provider, configPath string, rest []string, code int) {
	for len(args) > 0 {
		switch {
		case args[0] == "--config" && len(args) > 1:
			configPath, args = args[1], args[2:]
		case args[0] == "-m" || args[0] == "--model":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "-m needs a model")
				return "", "", "", nil, 2
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
	return model, provider, configPath, args, -1
}

func isVersion(arg string) bool {
	return arg == "version" || arg == "--version" || arg == "-v" || arg == "-V"
}

func cmdStatus(cfg *config.Config) int {
	fmt.Println("version " + version.String())
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
		if p.IsThin() {
			state := "down"
			if gateway.Healthy(p) {
				state = "healthy"
			}
			fmt.Printf("%-10s claude: http://127.0.0.1:%d (%s)\n", "", gateway.Port(p), state)
		} else if p.AnthropicBase != "" {
			fmt.Printf("%-10s claude: %s\n", "", p.AnthropicBase)
		}
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

func cmdGateway(cfg *config.Config, args []string) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: graft gateway run --provider name")
		return 2
	}
	var name string
	for i := 1; i < len(args); i++ {
		if args[i] == "--provider" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "gateway run needs --provider")
		return 2
	}
	p, err := cfg.ProviderNamed(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := gateway.Serve(p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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

type pickerOption struct {
	provider      string
	providerLabel string
	ref           string
	label         string
}

func chooseModel(cfg *config.Config, provider string, in io.Reader, out io.Writer) (string, error) {
	if provider != "" {
		if _, err := cfg.ProviderNamed(provider); err != nil {
			return "", err
		}
	}
	var options []pickerOption
	for _, name := range cfg.Order {
		if provider != "" && name != provider {
			continue
		}
		p := cfg.Providers[name]
		for _, model := range p.Models {
			label := model.DisplayName
			if label == "" {
				label = model.Slug
			}
			options = append(options, pickerOption{
				provider:      name,
				providerLabel: p.Label,
				ref:           name + "/" + model.Slug,
				label:         label,
			})
		}
	}
	if len(options) == 0 {
		if provider != "" {
			return "", fmt.Errorf("provider %q has no models", provider)
		}
		return "", fmt.Errorf("no models configured")
	}
	if f, ok := in.(*os.File); ok && isTerminal(out) && term.IsTerminal(int(f.Fd())) {
		state, err := term.MakeRaw(int(f.Fd()))
		if err == nil {
			defer term.Restore(int(f.Fd()), state)
			return runModelPicker(options, in, out, colorEnabled())
		}
	}
	return chooseModelNumbered(options, in, out)
}

func chooseModelNumbered(options []pickerOption, in io.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, "Select a model:")
	for i, option := range options {
		fmt.Fprintf(out, "  %d) %-48s %s\n", i+1, option.ref, option.label)
	}
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprintf(out, "Model [1-%d], q to cancel: ", len(options))
		if !scanner.Scan() {
			return "", fmt.Errorf("selection cancelled")
		}
		answer := strings.TrimSpace(scanner.Text())
		if answer == "" || strings.EqualFold(answer, "q") || strings.EqualFold(answer, "quit") {
			return "", fmt.Errorf("selection cancelled")
		}
		n, err := strconv.Atoi(answer)
		if err == nil && n >= 1 && n <= len(options) {
			return options[n-1].ref, nil
		}
		fmt.Fprintln(out, "Invalid selection.")
	}
}

func runModelPicker(options []pickerOption, in io.Reader, out io.Writer, color bool) (string, error) {
	selected := 0
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h")
	lines := renderModelPicker(out, options, selected, color)

	keys := bufio.NewReader(in)
	for {
		key, err := readPickerKey(keys)
		if err != nil {
			clearModelPicker(out, lines)
			return "", fmt.Errorf("selection cancelled")
		}
		switch key {
		case pickerUp:
			selected = (selected + len(options) - 1) % len(options)
		case pickerDown:
			selected = (selected + 1) % len(options)
		case pickerChoose:
			clearModelPicker(out, lines)
			if color {
				fmt.Fprintf(out, "\x1b[1;32m◆\x1b[0m \x1b[1;36m%s\x1b[0m\r\n", options[selected].ref)
			} else {
				fmt.Fprintf(out, "%s\r\n", options[selected].ref)
			}
			return options[selected].ref, nil
		case pickerCancel:
			clearModelPicker(out, lines)
			return "", fmt.Errorf("selection cancelled")
		}
		clearModelPicker(out, lines)
		lines = renderModelPicker(out, options, selected, color)
	}
}

type pickerKey int

const (
	pickerNoop pickerKey = iota
	pickerUp
	pickerDown
	pickerChoose
	pickerCancel
)

func readPickerKey(in *bufio.Reader) (pickerKey, error) {
	b, err := in.ReadByte()
	if err != nil {
		return pickerNoop, err
	}
	switch b {
	case 0x03, 'q', 'Q':
		return pickerCancel, nil
	case '\r', '\n':
		return pickerChoose, nil
	case 'k', 'K':
		return pickerUp, nil
	case 'j', 'J':
		return pickerDown, nil
	case 0x1b:
		next, err := in.ReadByte()
		if err != nil {
			return pickerCancel, nil
		}
		if next != '[' && next != 'O' {
			return pickerNoop, nil
		}
		for i := 0; i < 8; i++ {
			b, err = in.ReadByte()
			if err != nil {
				return pickerNoop, err
			}
			switch b {
			case 'A':
				return pickerUp, nil
			case 'B':
				return pickerDown, nil
			case 'H':
				return pickerNoop, nil
			case 'F':
				return pickerNoop, nil
			case '~', 'C', 'D':
				return pickerNoop, nil
			}
			if b >= 0x40 && b <= 0x7e {
				return pickerNoop, nil
			}
		}
	}
	return pickerNoop, nil
}

func renderModelPicker(out io.Writer, options []pickerOption, selected int, color bool) int {
	lines := 0
	width := pickerWidth(out)
	headline := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + "\x1b[0m"
	}

	fmt.Fprintf(out, "%s\r\n", headline("\x1b[1;36m", "Select a model"))
	fmt.Fprintf(out, "%s\r\n\r\n", headline("\x1b[2m", "↑/↓ move  ·  Enter select  ·  q cancel"))
	lines += 3

	provider := ""
	for i, option := range options {
		if option.provider != provider {
			provider = option.provider
			label := option.providerLabel
			if label == "" {
				label = option.provider
			}
			fmt.Fprintf(out, "  %s\r\n", headline("\x1b[1;34m", truncateRunes(label, width-2)))
			lines++
		}
		fmt.Fprintf(out, "%s\r\n", modelPickerRow(option, i == selected, color, width))
		lines++
	}
	return lines
}

func pickerWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width >= 40 {
			return width
		}
	}
	return 80
}

func modelPickerRow(option pickerOption, selected, color bool, width int) string {
	const refColumn = 46
	if width < 40 {
		width = 40
	}
	prefixPlain := "    "
	prefix := prefixPlain
	if selected {
		prefixPlain = "  › "
		prefix = "  \x1b[1;36m›\x1b[0m "
		if !color {
			prefix = prefixPlain
		}
	}
	bodyWidth := width - utf8.RuneCountInString(prefixPlain)
	refWidth := refColumn
	if bodyWidth < refWidth {
		refWidth = bodyWidth
	}
	ref := truncateRunes(option.ref, refWidth)
	if color {
		if selected {
			ref = "\x1b[1;36m" + ref + "\x1b[0m"
		} else {
			ref = "\x1b[1;37m" + ref + "\x1b[0m"
		}
	}
	if padding := refWidth - utf8.RuneCountInString(option.ref); padding > 0 {
		ref += strings.Repeat(" ", padding)
	}
	detailWidth := bodyWidth - refWidth - 1
	if detailWidth <= 0 {
		return prefix + ref
	}
	detail := truncateRunes(option.label, detailWidth)
	if detail != "" && color {
		if selected {
			detail = "\x1b[36m" + detail + "\x1b[0m"
		} else {
			detail = "\x1b[2m" + detail + "\x1b[0m"
		}
	}
	if detail == "" {
		return prefix + strings.TrimRight(ref, " ")
	}
	return prefix + ref + " " + detail
}

func truncateRunes(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func clearModelPicker(out io.Writer, lines int) {
	if lines > 0 {
		fmt.Fprintf(out, "\x1b[%dA", lines)
	}
	fmt.Fprint(out, "\r\x1b[J")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func colorEnabled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if os.Getenv("TERM") == "dumb" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	return true
}

func finish(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "graft %s runs Codex or Claude Code against a non-OpenAI provider.\n", version.String())
	fmt.Fprint(w, `
Usage:
  graft codex [codex args...]
  graft codex -m provider/model [codex args...]
  graft claude [claude args...]
  graft claude -m provider/model [claude args...]
  graft version
  graft init
  graft sync [-q]
  graft models
  graft status
  graft doctor
  graft proxy start|stop|health [--provider name]
  graft key set <slot> [value]
  graft key list

Without -m, an interactive terminal prompts for a model. -m, --provider and
--config before the client arguments are graft's; everything after them is
passed to codex or claude.
`)
}
