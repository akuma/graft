"""Model catalog generation.

codex's `/model` picker reads a model catalog, not the config's `model` value.
If a provider does not publish one, codex falls back to its bundled GPT list and
shows models that cannot work against graft's upstreams. Setting
`model_catalog_json` replaces the bundled list outright (verified with
`codex debug models`), so each graft profile gets a catalog containing only its
own provider's models.

`instructions_template` is mandatory for every entry: it becomes the model's
system prompt, so graft vendors codex's own published prompt instead of
inventing a weaker one. See data/codex_instructions.txt for provenance.
"""

from __future__ import annotations

import json
from pathlib import Path

from .config import Config, Provider

DATA_DIR = Path(__file__).resolve().parent.parent / "data"
INSTRUCTIONS = DATA_DIR / "codex_instructions.txt"

# Field template mirroring codex's bundled catalog entries. Values that depend
# on the upstream live in PROVIDER_DEFAULTS below.
BASE_ENTRY = {
    "shell_type": "unified_exec",
    "visibility": "list",
    "supported_in_api": True,
    "priority": 1,
    "include_skills_usage_instructions": False,
    "include_plugin_usage_instructions": False,
    "include_apps_usage_instructions": False,
    "default_reasoning_summary": "none",
    "support_verbosity": True,
    "default_verbosity": "low",
    "truncation_policy": {"mode": "tokens", "limit": 10000},
    "comp_hash": "3000",
    "effective_context_window_percent": 95,
    "input_modalities": ["text"],
    "supports_experimental_context": False,
    "prefer_websockets": False,
    "availability_nux": None,
    "upgrade": None,
    "auto_review_model_override": None,
    "experimental_supported_tools": [],
    "supports_parallel_tool_calls": True,
}

# Upstream specific capability flags. Defaults are the conservative set: no
# freeform apply_patch (some upstreams only accept `function` tools), no
# standalone search tool, and responses-lite off (it strips every tool).
CONSERVATIVE = {
    "apply_patch_tool_type": None,
    "web_search_tool_type": "text",
    "supports_search_tool": False,
    "use_responses_lite": False,
    "tool_mode": None,
    "multi_agent_version": None,
    "supports_reasoning_summaries": False,
    "reasoning_summary_format": "experimental",
}

PROVIDER_DEFAULTS: dict[str, dict] = {
    # DeepSeek publishes a catalog for codex; mirror its flags.
    "deepseek": {
        "apply_patch_tool_type": "freeform",
        "web_search_tool_type": "text",
        "supports_search_tool": True,
        "use_responses_lite": False,
        "tool_mode": None,
        "multi_agent_version": "v2",
        "supports_reasoning_summaries": True,
        "reasoning_summary_format": "experimental",
        "shell_type": "shell_command",
    },
}

REASONING_LEVELS = [
    {"effort": "low", "description": "Fast responses with lighter reasoning"},
    {"effort": "medium", "description": "Balanced reasoning for everyday work"},
    {"effort": "high", "description": "Extra high reasoning depth for complex problems"},
]


def _instructions() -> str:
    if INSTRUCTIONS.exists():
        return INSTRUCTIONS.read_text(encoding="utf-8")
    return "You are Codex, an autonomous coding agent. Complete the user's request."


def entry_for(provider: Provider, slug: str) -> dict:
    flags = dict(CONSERVATIVE)
    flags.update(PROVIDER_DEFAULTS.get(provider.name, {}))
    # only known catalog flags may be overridden from config; a stray key must
    # not land in the JSON codex parses
    overridable = set(CONSERVATIVE) | set(BASE_ENTRY)
    for key, value in (provider.extra or {}).items():
        if key in overridable and (value is not None or key == "apply_patch_tool_type"):
            flags[key] = value
    entry = dict(BASE_ENTRY)
    entry.update(flags)
    entry["slug"] = slug
    entry["display_name"] = _display_name(provider, slug)
    entry["description"] = f"{provider.label} via graft"
    entry["default_reasoning_level"] = "high"
    entry["supported_reasoning_levels"] = REASONING_LEVELS
    entry["model_messages"] = {"instructions_template": _instructions()}
    if provider.context_window:
        entry["context_window"] = provider.context_window
        entry["max_context_window"] = provider.context_window
    return entry


def _display_name(provider: Provider, slug: str) -> str:
    labels = {
        "grok-4.7": "Grok 4.7",
        "grok-4.6": "Grok 4.6",
        "deepseek-flash": "DeepSeek Flash",
        "deepseek-v4-pro": "DeepSeek V4 Pro",
    }
    return labels.get(slug, slug)


def catalog_path(config: Config, provider: Provider) -> Path:
    return config.codex_home / f"{provider.profile_name(config.profile_prefix)}-models.json"


def build(config: Config, provider: Provider) -> dict:
    return {"models": [entry_for(provider, slug) for slug in provider.models]}


def write(config: Config, provider: Provider, verbose: bool = True) -> Path | None:
    if not provider.models:
        return None
    path = catalog_path(config, provider)
    payload = json.dumps(build(config, provider), ensure_ascii=False, indent=1)
    if path.exists() and path.read_text(encoding="utf-8") == payload:
        return path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(payload, encoding="utf-8")
    if verbose:
        print(f"wrote {path}")
    return path
