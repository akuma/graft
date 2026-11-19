"""Configuration loading for graft.

Layout:
  ~/.config/graft/config.toml   user config (providers, ports)
  ~/.config/graft/secrets.toml  api keys, mode 0600
  ~/.config/graft/run/          pid + log files for thin proxies

Every provider is declared in config, so adding an upstream is a config edit
plus a `graft sync`. No code change required for direct transports.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

DEFAULT_CONFIG = """\
[general]
# codex home; profiles are written next to the user config.toml
codex_home = "~/.codex"
# thin proxies listen on proxy_port + provider_index
proxy_port = 8787
# base codex config is never modified; profiles layer on top of it
profile_prefix = "graft-"

# codex's provider entry needs an env var to hold its bearer token; graft sets it
# automatically as GRAFT_<PROVIDER>_KEY when a provider does not declare env_key.

[providers.grok]
label = "Grok Build"
# thin | direct
transport = "thin"
upstream = "https://cli-chat-proxy.grok.com/v1"
# borrow | api-key : borrow reads a CLI subscription token at launch/request time
auth = "borrow"
auth_source = "grok-cli"
context_window = 256000
# grok's proxy rejects codex multi-agent namespaces and strict web_search args
disable_agent_tools = true
disable_web_search = true
models = ["grok-4.7", "grok-4.6"]

[providers.deepseek]
label = "DeepSeek"
transport = "direct"
upstream = "https://api.deepseek.com/"
auth = "api-key"
# graft reads the key from $DEEPSEEK_API_KEY, else ~/.config/graft/secrets.toml:[keys].deepseek
key_env = "DEEPSEEK_API_KEY"
secrets_key = "deepseek"
context_window = 1048576
disable_agent_tools = false
disable_web_search = false
models = ["deepseek-flash", "deepseek-v4-pro"]
"""


def _expand(value: str) -> str:
    return os.path.expanduser(os.path.expandvars(value))


def config_home() -> Path:
    env = os.environ.get("GRAFT_CONFIG_HOME")
    if env:
        return Path(_expand(env))
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return Path(base) / "graft"


@dataclass
class Provider:
    name: str
    label: str
    transport: str
    upstream: str
    auth: str
    models: list[str]
    env_key: str | None = None
    context_window: int | None = None
    auth_source: str | None = None
    key_env: str | None = None
    secrets_key: str | None = None
    disable_agent_tools: bool = False
    disable_web_search: bool = False
    port: int | None = None
    extra: dict = field(default_factory=dict)

    @property
    def is_thin(self) -> bool:
        return self.transport == "thin"

    def profile_name(self, prefix: str) -> str:
        return prefix + self.name

    def env_var(self) -> str:
        # internal: the env var codex reads its bearer token from
        return self.env_key or ("GRAFT_" + self.name.upper().replace("-", "_") + "_KEY")


@dataclass
class Config:
    path: Path
    codex_home: Path
    proxy_port: int
    profile_prefix: str
    providers: dict[str, Provider]
    raw: dict

    def provider(self, name: str) -> Provider:
        if name not in self.providers:
            known = ", ".join(sorted(self.providers)) or "<none>"
            raise SystemExit(f"unknown provider '{name}' (known: {known})")
        return self.providers[name]

    def resolve_model(self, model: str | None, provider: str | None) -> tuple[Provider, str | None]:
        """Model name wins over provider; provider fallback uses its default model."""
        if model:
            normalized = model.split(":", 1)[-1]
            for prov in self.providers.values():
                if normalized in prov.models:
                    return prov, normalized
            known = ", ".join(m for p in self.providers.values() for m in p.models)
            raise SystemExit(f"unknown model '{model}' (known: {known})")
        if provider:
            prov = self.provider(provider)
            return prov, (prov.models[0] if prov.models else None)
        raise SystemExit("no model given: pass -m <model> or --provider <name>")


def load(path: Path | None = None) -> Config:
    cfg_path = path or Path(os.environ.get("GRAFT_CONFIG") or config_home() / "config.toml")
    if not cfg_path.exists():
        cfg_path.parent.mkdir(parents=True, exist_ok=True)
        cfg_path.write_text(DEFAULT_CONFIG, encoding="utf-8")
    raw = tomllib.loads(cfg_path.read_text(encoding="utf-8"))

    general = raw.get("general") or {}
    codex_home = Path(_expand(general.get("codex_home", "~/.codex")))
    proxy_port = int(general.get("proxy_port", 8787))
    prefix = general.get("profile_prefix", "graft-")

    providers: dict[str, Provider] = {}
    index = 0
    for name, spec in (raw.get("providers") or {}).items():
        known = {
            "label", "transport", "upstream", "auth", "models", "env_key",
            "context_window", "auth_source", "key_env", "secrets_key",
            "disable_agent_tools", "disable_web_search", "port",
        }
        kwargs = {k: v for k, v in spec.items() if k in known}
        extra = {k: v for k, v in spec.items() if k not in known}
        kwargs.setdefault("label", name)
        kwargs.setdefault("transport", "direct")
        kwargs.setdefault("models", [])
        kwargs.setdefault("auth", "api-key")
        if kwargs.get("port") is None:
            kwargs["port"] = proxy_port + index
        providers[name] = Provider(name=name, extra=extra, **kwargs)
        index += 1

    return Config(
        path=cfg_path, codex_home=codex_home, proxy_port=proxy_port,
        profile_prefix=prefix, providers=providers, raw=raw,
    )
