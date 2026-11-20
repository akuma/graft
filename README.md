# graft

Run [Codex](https://github.com/openai/codex) or [Claude Code](https://code.claude.com) against other models without touching the client you already use. Skills, hooks, MCP, and sessions stay where they are. `~/.codex/config.toml` and `~/.claude/settings.json` are never modified.

| Model | Upstream | How |
| --- | --- | --- |
| `grok-4.7`, `grok-4.6` | Grok Build subscription | Local proxy. Uses the Grok CLI login, not xAI API billing. |
| `deepseek-flash`, `deepseek-v4-pro` | DeepSeek API | Direct. No extra process. |

Plain `codex` still shows the built-in GPT list. A graft session only lists that provider's models.

Each model’s `input` and `reasoning` are read from `config.toml`. Codex allows a pasted image or `view_image` only when `input` includes `image`. The shipped file sets that for `grok-4.7`, `grok-4.6`, and `deepseek-flash`. `deepseek-v4-pro` is `input = ["text"]`. A bare `models = ["id"]` list stays text-only, with reasoning `low`, `medium`, and `high`. Graft does not rewrite image parts. `supports_image_detail_original` stays false, so Codex sends its resized JPEG or PNG.

## Requirements

- Codex CLI
- Grok: [Grok CLI](https://x.ai) signed in (`grok login`)
- DeepSeek: an API key

Go is only needed to build from source.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/akuma/graft/main/install.sh | sh
graft init
```

This downloads the latest release binary into `~/.local/bin`. `GRAFT_VERSION=v0.1.0` pins a version. `GRAFT_BIN` changes the directory.

From a checkout: `go build -o ~/.local/bin/graft ./cmd/graft`, or `go install github.com/akuma/graft/cmd/graft@latest`.

`graft init` writes `~/.config/graft/config.toml` and one Codex profile per provider (`~/.codex/graft-<provider>.config.toml`). It does not write `~/.codex/config.toml` or `~/.codex/models.json`.

## Use

```sh
graft codex -m grok-4.7
graft codex -m grok-4.6
graft codex -m deepseek-flash
graft codex -m deepseek-v4-pro exec "fix the bug"
graft claude -m grok-4.7
graft claude -m deepseek-flash
graft claude -m deepseek-v4-pro
```

`-m`, `--provider`, and `--config` are graft's. Everything after them is passed to `codex` or `claude`.

```sh
export DEEPSEEK_API_KEY=sk-...          # or: graft key set deepseek
graft version
graft status
graft doctor
graft models
graft sync                              # rewrite profiles after editing config
graft proxy start|stop|health
```

A Grok Codex session starts the proxy on `127.0.0.1:8787` and stops it when Codex exits. DeepSeek does not start one. `graft proxy start` keeps a proxy running until `graft proxy stop`. The proxy re-reads `~/.grok/auth.json` on each request and never writes that file.

## Claude Code

`graft claude -m <model>` starts Claude Code on the same four models. It passes a one-session `--settings` file, which outranks `~/.claude/settings.json` and project settings for that process. The file is rewritten each launch and is not your Claude config.

DeepSeek uses the official Messages endpoint, `https://api.deepseek.com/anthropic`, and the same `DEEPSEEK_API_KEY`. Opus, Sonnet, Haiku, Fable, and the subagent alias are pinned to the model you named, so a picker row cannot fall through to DeepSeek's `claude-opus*` → `deepseek-v4-pro` mapping. A 1M model is pinned with the `[1m]` suffix. Claude Code strips that before the request. Auto-compact is three quarters of the provider window, which is `786432` for DeepSeek. Effort is `max` when the model lists it.

Grok's subscription endpoint speaks Responses, not Messages. `graft claude -m grok-4.7` starts a Messages gateway on `127.0.0.1:8887` that translates the request, applies the same rewrites as the Codex proxy, and carries reasoning `encrypted_content` back inside the thinking signature. The gateway reads the Grok CLI login itself. Claude's own login is left in place: the session sets `ANTHROPIC_AUTH_TOKEN` empty, so claude.ai connectors are not disabled. Effort is `high`, and the window is `500000`. Claude Code would otherwise label that unrecognized id as a 1M model, so the session sets `CLAUDE_CODE_DISABLE_1M_CONTEXT`. The gateway stops when Claude Code exits. A gateway that was already running is left alone, and it does not share a port or pid with the Codex proxy.

`/model` stays on the model you started. Switching provider or model is a new `graft claude -m ...`.

## Why Grok needs a proxy

Codex speaks the Responses API. DeepSeek accepts that directly. Grok's subscription proxy rejects a few shapes Codex sends, so graft rewrites those and nothing else:

- `tools[].type = "namespace"` (multi-agent and MCP bundles) → 422
- extra fields on `web_search`, such as `external_web_access` → 400
- `"content": null` on a replayed `reasoning` item → 400 (`Could not decode the compaction blob`). The item, its summary, and `encrypted_content` stay
- `tool_choice` when `tools` is missing or empty (local compaction sends `tools: []` with `tool_choice: "auto"`) → 400

Rules live in `internal/transform`.

## Add a provider

Edit `~/.config/graft/config.toml`, then run `graft sync`. `direct` means the upstream already accepts Codex's Responses requests. `thin` means graft must rewrite them; the Grok rules are `transform.ForGrok`.

```toml
[providers.kimi]
label = "Kimi"
transport = "direct"
upstream = "https://api.moonshot.cn/v1"
auth = "api-key"
key_env = "MOONSHOT_API_KEY"
context_window = 262144
models = ["kimi-k2"]

# or, when the model accepts images or a different effort list:
# [[providers.kimi.models]]
# id = "kimi-k2"
# display_name = "Kimi K2"
# input = ["text"]
# reasoning = ["low", "high"]
```

Confirm the upstream actually implements `/responses` before adding it. A Chat Completions endpoint will not work.

## Limits

- With Grok, MCP and multi-agent tools are dropped. Shell and file tools still work.
- Reasoning is not carried across turns on Grok, so prompt-cache hits are lower.
- `/model` cannot switch providers. `grok-4.7` and `deepseek-flash` use different endpoints and credentials; start a new `graft codex -m ...` session.
- Grok Build access is sold for the Grok CLI. Pointing Codex at it may fall outside that product's terms. DeepSeek is normal API billing.

## Tests

```sh
go test ./...
```

## License

[MIT](LICENSE)
