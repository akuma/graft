"""Credential resolution.

Two shapes:
  api-key : a long lived key from env or ~/.config/graft/secrets.toml
  borrow  : a CLI subscription token, read at request time from the CLI's own
            auth store. graft never writes those files; it only reads them, so
            the CLI keeps owning refresh.
"""

from __future__ import annotations

import json
import os
import time
import tomllib
from pathlib import Path

from .config import config_home, Provider


class AuthError(SystemExit):
    pass


def secrets_path() -> Path:
    return config_home() / "secrets.toml"


def load_secrets() -> dict:
    path = secrets_path()
    if not path.exists():
        return {}
    try:
        return tomllib.loads(path.read_text(encoding="utf-8"))
    except tomllib.TOMLDecodeError as exc:
        raise AuthError(f"{path} is not valid TOML: {exc}")


def save_secret(name: str, value: str) -> Path:
    """Store an api key with 0600 permissions, preserving other entries."""
    path = secrets_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    secrets = load_secrets()
    keys = secrets.setdefault("keys", {})
    keys[name] = value
    lines = ["# graft secrets, keep this file private", "[keys]"]
    for k, v in keys.items():
        lines.append(f'{k} = {json.dumps(v)}')
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    path.chmod(0o600)
    return path


def api_key(provider: Provider) -> str:
    if provider.key_env and os.environ.get(provider.key_env):
        return os.environ[provider.key_env]
    secrets = load_secrets().get("keys") or {}
    slot = provider.secrets_key or provider.name
    if slot in secrets and secrets[slot]:
        return secrets[slot]
    looked = [p for p in (provider.key_env, f"{secrets_path()}:[keys].{slot}") if p]
    raise AuthError(
        f"no api key for provider '{provider.name}'. Set {provider.key_env or 'an env var'} "
        f"or run: graft key set {slot}\nlooked at: {', '.join(looked)}"
    )


GROK_AUTH = "~/.grok/auth.json"
GROK_VERSION = "~/.grok/version.json"


def _read_json(path: str) -> dict:
    expanded = Path(os.path.expanduser(path))
    if not expanded.exists():
        raise AuthError(f"{expanded} not found; run: grok login")
    try:
        return json.loads(expanded.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise AuthError(f"{expanded} is not valid JSON: {exc}")


def grok_session() -> dict:
    """Return the freshest Grok CLI subscription credential.

    auth.json is a flat map of `issuer::client_id` -> credential. Multiple
    accounts may coexist; pick the newest non-expired one.
    """
    store = _read_json(GROK_AUTH)
    candidates = []
    for entry in store.values():
        if isinstance(entry, dict) and entry.get("key"):
            candidates.append(entry)
    if not candidates:
        raise AuthError("no subscription token in ~/.grok/auth.json; run: grok login")

    def expires_at(entry: dict) -> float:
        for field in ("expires_at",):
            raw = entry.get(field)
            if isinstance(raw, (int, float)):
                return float(raw)
            if isinstance(raw, str):
                try:
                    from datetime import datetime

                    return datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp()
                except ValueError:
                    pass
        return 0.0

    live = [e for e in candidates if expires_at(e) > time.time()]
    pool = live or candidates
    entry = max(pool, key=expires_at)
    return {
        "key": entry["key"],
        "email": entry.get("email"),
        "expires_at": expires_at(entry),
        "expired": expires_at(entry) <= time.time() if expires_at(entry) else False,
    }


def grok_client_version(fallback: str = "1.0.89") -> str:
    try:
        version = _read_json(GROK_VERSION).get("version")
    except AuthError:
        return fallback
    return str(version or fallback)


def check(provider: Provider) -> list[str]:
    """Non-fatal preflight notes for a provider."""
    notes: list[str] = []
    if provider.auth == "borrow":
        if provider.auth_source == "grok-cli":
            session = grok_session()
            if session["expired"]:
                notes.append(
                    "grok subscription token expired; run `grok login` "
                    f"(account: {session.get('email') or 'unknown'})"
                )
        else:
            notes.append(f"unknown auth_source '{provider.auth_source}'")
    elif provider.auth == "api-key":
        try:
            api_key(provider)
        except AuthError as exc:
            notes.append(str(exc))
    else:
        notes.append(f"unknown auth '{provider.auth}'")
    return notes
