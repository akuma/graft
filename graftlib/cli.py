"""graft command line."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from . import codex, creds, proxy
from .config import Config, load


def _cmd_init(args: argparse.Namespace, config: Config) -> int:
    print(f"config: {config.path}")
    codex.sync(config)
    return 0


def _cmd_sync(args: argparse.Namespace, config: Config) -> int:
    written = codex.sync(config, verbose=not args.quiet)
    if args.quiet:
        print(f"synced {len(written)} profile(s)")
    return 0


def _cmd_codex(args: argparse.Namespace, config: Config) -> int:
    provider, model = config.resolve_model(args.model, args.provider)
    codex.launch(config, provider, model, args.codex_args)
    return 0  # unreachable, exec replaces the process


def _cmd_proxy(args: argparse.Namespace, config: Config) -> int:
    providers = [config.provider(args.provider)] if args.provider else list(config.providers.values())
    for provider in providers:
        if not provider.is_thin:
            print(f"{provider.name}: transport=direct, no proxy needed")
            continue
        if args.action == "start":
            proxy.ensure(config, provider)
        elif args.action == "stop":
            print(f"{provider.name}: {'stopped' if proxy.stop(provider) else 'not running'}")
        elif args.action == "health":
            print(f"{provider.name}: {'healthy' if proxy.healthy(provider) else 'down'} (port {provider.port})")
    return 0


def _cmd_status(args: argparse.Namespace, config: Config) -> int:
    print(f"config  {config.path}")
    print(f"codex   {config.codex_home}")
    print("")
    for provider in config.providers.values():
        state = ""
        if provider.is_thin:
            state = "healthy" if proxy.healthy(provider) else "down"
        profile = codex.profile_path(config, provider)
        exists = "profile ok" if profile.exists() else "profile MISSING (run: graft sync)"
        print(f"{provider.name:<10} {provider.transport:<7} {exists:<12} proxy={state or '-'}")
        print(f"{'':<10} models: {', '.join(provider.models)}")
        print(f"{'':<10} base_url: {provider.upstream}")
    return 0


def _cmd_key_list(args: argparse.Namespace, config: Config) -> int:
    keys = (creds.load_secrets().get("keys") or {})
    if not keys:
        print("no keys stored")
    for slot, value in keys.items():
        print(f"{slot:<12} stored ({len(value)} chars)")
    return 0


def _cmd_key(args: argparse.Namespace, config: Config) -> int:
    slot = args.slot
    if args.value:
        value = args.value
    else:
        if sys.stdin.isatty():
            print(f"paste key for '{slot}' then press enter: ", end="", file=sys.stderr, flush=True)
        value = sys.stdin.readline().strip()
    if not value:
        raise SystemExit("empty key, nothing written")
    path = creds.save_secret(slot, value)
    print(f"stored {slot} in {path} (mode 600)")
    return 0


def _cmd_doctor(args: argparse.Namespace, config: Config) -> int:
    problems = 0
    for provider in config.providers.values():
        print(f"[{provider.name}]")
        for note in creds.check(provider):
            print(f"  ! {note}")
            problems += 1
        if provider.auth == "borrow" and provider.auth_source == "grok-cli":
            try:
                session = creds.grok_session()
                expiry = "expired" if session["expired"] else "ok"
                print(f"  token {expiry} for {session.get('email') or 'unknown'}")
            except creds.AuthError as exc:
                print(f"  ! {exc}")
                problems += 1
        if provider.is_thin:
            print(f"  proxy {'healthy' if proxy.healthy(provider) else 'down'} (port {provider.port})")
    return 1 if problems else 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="graft", description="Run codex against non-OpenAI LLM subscriptions.")
    parser.add_argument("--config", help="path to graft config.toml")
    sub = parser.add_subparsers(dest="command", required=True)

    p_init = sub.add_parser("init", help="create config if missing and write codex profiles")
    p_init.set_defaults(func=_cmd_init)

    p_sync = sub.add_parser("sync", help="(re)generate codex profile files")
    p_sync.add_argument("-q", "--quiet", action="store_true")
    p_sync.set_defaults(func=_cmd_sync)

    p_codex = sub.add_parser("codex", help="launch codex with a graft provider")
    p_codex.add_argument("-m", "--model", help="model to use (e.g. grok-4.7, deepseek-flash)")
    p_codex.add_argument("--provider", help="provider name when no model is given")
    p_codex.add_argument("codex_args", nargs=argparse.REMAINDER, help="args passed to codex (prefix with --)")
    p_codex.set_defaults(func=_cmd_codex)

    p_proxy = sub.add_parser("proxy", help="manage thin proxies")
    p_proxy.add_argument("action", choices=["start", "stop", "health"])
    p_proxy.add_argument("--provider")
    p_proxy.set_defaults(func=_cmd_proxy)

    p_status = sub.add_parser("status", help="show providers, profiles and proxy state")
    p_status.set_defaults(func=_cmd_status)

    p_key = sub.add_parser("key", help="manage api keys in secrets.toml")
    key_sub = p_key.add_subparsers(dest="key_cmd", required=True)
    p_key_set = key_sub.add_parser("set", help="store an api key")
    p_key_set.add_argument("slot", help="key slot, e.g. deepseek")
    p_key_set.add_argument("value", nargs="?", help="omit to read from stdin")
    p_key_set.set_defaults(func=_cmd_key)
    p_key_list = key_sub.add_parser("list", help="show which slots are stored")
    p_key_list.set_defaults(func=_cmd_key_list)

    p_doctor = sub.add_parser("doctor", help="check credentials and proxy health")
    p_doctor.set_defaults(func=_cmd_doctor)

    p_models = sub.add_parser("models", help="list model -> provider mapping")
    p_models.set_defaults(func=_cmd_models)

    return parser


def _cmd_models(args: argparse.Namespace, config: Config) -> int:
    for provider in config.providers.values():
        for model in provider.models:
            print(f"{model:<18} -> {provider.name} ({provider.transport})")
    return 0


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    # `graft codex exec ...` must not swallow codex flags, so stop argparse at
    # the first codex argument ourselves.
    parser = build_parser()
    if argv and argv[0] == "codex":
        rest = argv[1:]
        model = provider = config_path = None
        while rest:
            token = rest[0]
            if token == "--config" and len(rest) > 1:
                config_path, rest = rest[1], rest[2:]
                continue
            if token in ("-m", "--model") and len(rest) > 1:
                model, rest = rest[1], rest[2:]
                continue
            if token.startswith("--model="):
                model, rest = token.split("=", 1)[1], rest[1:]
                continue
            if token == "--provider" and len(rest) > 1:
                provider, rest = rest[1], rest[2:]
                continue
            break
        if rest and rest[0] == "--":
            rest = rest[1:]
        args = argparse.Namespace(
            command="codex", model=model, provider=provider,
            codex_args=rest, config=config_path, func=_cmd_codex,
        )
        config = load(Path(config_path) if config_path else None)
        return args.func(args, config)

    args = parser.parse_args(argv)
    config = load(Path(args.config) if args.config else None)
    return args.func(args, config)
