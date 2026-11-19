# graft

Run [Codex](https://github.com/openai/codex) against other models without touching your normal Codex setup. Skills, `AGENTS.md`, hooks, MCP, and sessions stay where they are. `~/.codex/config.toml` is never modified.

| Model | Upstream | How |
| --- | --- | --- |
| `grok-4.7`, `grok-4.6` | Grok Build subscription | Local proxy. Uses the Grok CLI login, not xAI API billing. |
| `deepseek-flash`, `deepseek-v4-pro` | DeepSeek API | Direct. No extra process. |

Plain `codex` still shows the built-in GPT list. A graft session only lists that provider's models.

## Requirements

- Go 1.23+
- Codex CLI
- Grok: [Grok CLI](https://x.ai) signed in (`grok login`)
- DeepSeek: an API key

## Install

```sh
git clone https://github.com/akuma/graft.git
cd graft
go build -o ~/.local/bin/graft ./cmd/graft
graft init
```

Once the repository is public, `go install github.com/akuma/graft/cmd/graft@latest` does the same thing.

`graft init` writes `~/.config/graft/config.toml` and one Codex profile per provider (`~/.codex/graft-<provider>.config.toml`). It does not write `~/.codex/config.toml` or `~/.codex/models.json`.

## Use

```sh
graft codex -m grok-4.7
graft codex -m grok-4.6
graft codex -m deepseek-flash
graft codex -m deepseek-v4-pro exec "fix the bug"
```

`-m`, `--provider`, and `--config` are graft's. Everything after them is passed to `codex`.

```sh
export DEEPSEEK_API_KEY=sk-...          # or: graft key set deepseek
graft status
graft doctor
graft models
graft sync                              # rewrite profiles after editing config
graft proxy start|stop|health
```

The Grok proxy starts on demand and listens on `127.0.0.1:8787`. It re-reads `~/.grok/auth.json` on each request and never writes that file.

## Why Grok needs a proxy

Codex speaks the Responses API. DeepSeek accepts that directly. Grok's subscription proxy does not accept three things Codex always sends, so graft strips them and nothing else:

- `tools[].type = "namespace"` (multi-agent and MCP bundles) → 422
- extra fields on `web_search`, such as `external_web_access` → 400
- replayed `reasoning` items → 400 (`Could not decode the compaction blob`)

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
