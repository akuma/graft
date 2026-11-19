# graft

把 codex 接到非 OpenAI 的模型上，同时保留你现有的 codex 配置（skills、AGENTS.md、hooks、MCP、会话）。

现在支持：

| 模型 | provider | 传输 | 说明 |
| --- | --- | --- | --- |
| `grok-4.7` / `grok-4.6` | Grok Build 订阅 | thin proxy | 借用 Grok CLI 的订阅凭证，不按 API 计费 |
| `deepseek-flash` / `deepseek-v4-pro` | DeepSeek | direct | 纯配置，零常驻进程 |

## 为什么不直接用 codex 的配置

codex 0.170 只会说 Responses 协议（`wire_api = "chat"` 已被删除）。它的请求里有三样东西，Grok 的订阅代理会拒绝：

| codex 发的 | Grok 的反应 | graft 的处理 |
| --- | --- | --- |
| `tools[].type = "namespace"`（多 agent 与 MCP 工具包） | `422 unknown variant namespace` | 丢弃 |
| `web_search` 带 `external_web_access` | `400 Argument not supported` | 削成 `{"type":"web_search"}` |
| `input[]` 回灌的 `reasoning` 项 | `400 Could not decode the compaction blob` | 从输入中剔除 |

这三条实测得来，写在 `graftlib/transform.py`，并有单元测试守着。DeepSeek 原生实现了 Responses 服务端，所以不需要这一层。

## 安装

```sh
ln -sf ~/Programs/tools/graft/bin/graft ~/.local/bin/graft
graft init          # 生成 ~/.config/graft/config.toml 和 ~/.codex/graft-*.config.toml
```

`graft init` 只新增两份 profile 文件，`~/.codex/config.toml`、`~/.codex/models.json` 一个字节都不动。正常 `codex` 的行为、默认模型、会话、skills 全部照旧。

## 用法

```sh
graft codex -m grok-4.7                 # 交互式 TUI，自动拉起 thin proxy
graft codex -m grok-4.6
graft codex -m deepseek-flash           # direct，不起进程
graft codex -m deepseek-v4-pro exec "改这个 bug"   # codex 参数原样透传
graft codex --provider grok             # 不给模型时用 provider 的默认模型
```

`-m/--model`、`--provider`、`--config` 由 graft 解析，其余参数一律透传给 codex，所以 `exec`、`--skip-git-repo-check`、`-C` 都能用。

其它命令：

```sh
graft status              # provider / profile / proxy 状态
graft models              # 模型到 provider 的映射
graft doctor              # 凭证与 proxy 健康检查
graft sync                # 改完 config.toml 后重写 profile
graft key set deepseek    # 把 API key 存进 ~/.config/graft/secrets.toml (0600)
graft proxy start|stop|health
```

curl 直接打 gateway 也可以：

```sh
curl -s http://127.0.0.1:8787/v1/responses -H 'content-type: application/json' -d '{"model":"grok-4.7","input":"hi"}'
```

## 凭证

- **Grok 订阅**：每次请求时重读 `~/.grok/auth.json`，token 由 `grok login` 维护，graft 从不写这个文件。`x-grok-client-version` 取自 `~/.grok/version.json`，`grok update` 后自动跟随，不会因为版本门禁吃 426。上游回 401/426 时会重读凭证重试一次。
- **DeepSeek**：`DEEPSEEK_API_KEY` 优先，其次 `~/.config/graft/secrets.toml` 的 `[keys].deepseek`。key 不会写进 `~/.codex` 下的任何文件。

### 两处 key 相关字段的区别

| 字段 | 谁读 | 你需不需要管 |
| --- | --- | --- |
| `key_env` | **graft** 从这里读真实 key（`DEEPSEEK_API_KEY`） | 要管：`export` 或 `graft key set deepseek`，二选一 |
| `secrets_key` | graft 读 `secrets.toml` 时用的槽名 | 不用管，默认就是 provider 名 |
| `env_key` | **codex** 从哪个环境变量取它的 bearer token；graft 在启动子进程时填进去 | 不用管，默认按 provider 名自动派生为 `GRAFT_<PROVIDER>_KEY` |

也就是说：你只需提供一个真实 key（env 或 secrets 文件），`env_key` 是 graft 与 codex 之间的内部接线，配置里不必出现。thin 传输下这个变量里放的是占位符，真凭证由网关在每次请求时注入。

## 加一个新 provider

只改 `~/.config/graft/config.toml`，然后 `graft sync`：

```toml
[providers.kimi]
label = "Kimi"
transport = "direct"
upstream = "https://api.moonshot.cn/v1"   # 需要上游自己实现 Responses
auth = "api-key"
key_env = "MOONSHOT_API_KEY"
secrets_key = "kimi"
context_window = 262144
models = ["kimi-k2"]
```

- `transport = "direct"`：客户端能指向上游并由上游原样接受，graft 只做配置与凭证注入。
- `transport = "thin"`：需要改字节时用。规则在 `graftlib/transform.py` 的 `TRANSPORTS` 里注册，纯函数、可测试。

新增一格前先用 `curl` 把上游打一遍，别靠猜：本次三条规则都是错误码反推出来的。

## 已知限制

- **MCP 工具在 grok 下不可用。** codex 把 MCP 工具包在 `type: namespace` 里，Grok 的代理不接受，只能整体丢弃。工具调用和文件编辑不受影响，走 codex 原生 function 工具。
- **跨轮推理上下文不连续。** 回灌的 `reasoning` blob 是上游签的，重新序列化就对不上，只能剔除。prompt cache 命中率因此略低。
- **grok 多 agent 子工具不可见**，profile 里用 `[agents] enabled = false` 关掉。
- **`/model` 只列出当前 provider 的模型。** codex 的模型选择器读的是模型目录，不读配置里的 `model`；不给目录时它回落到内置的 GPT 列表。`graft sync` 为每个 provider 写一份独立目录（`~/.codex/graft-<provider>-models.json`），profile 用 `model_catalog_json` 指向它，因此 `graft codex -m grok-4.7` 里只能看到 `grok-4.7` / `grok-4.6`。换 provider 要重新启动（`graft codex -m deepseek-flash`），因为两边的 `base_url` 和凭证不同，同一次会话切不过去。`~/.codex/models.json` 仍留给 DeepSeek 官方脚本，graft 不写它。
- **订阅套壳的合规风险**：Grok Build 订阅是卖给 Grok CLI 的，从 codex 调用属于灰度地带；DeepSeek 按 API 计费，无此问题。

## 测试

```sh
python3 -m unittest discover -s tests -v
```

端到端验证（真实调用，会计费/计额度）：

```sh
graft codex -m grok-4.7 exec --skip-git-repo-check "run: echo ok; reply DONE"
graft codex -m deepseek-flash exec --skip-git-repo-check "run: echo ok; reply DONE"
```

## 结构

```
bin/graft                 # 启动器
graftlib/config.py        # 配置与 provider 声明
graftlib/creds.py         # api key 与 CLI 订阅凭证读取
graftlib/transform.py     # thin 传输的字节规则（纯函数）
graftlib/proxy.py         # thin proxy：改字节 + 注入凭证，不做协议翻译
graftlib/codex.py         # profile 生成与 codex 启动
graftlib/cli.py           # 命令行
tests/test_transform.py
```
