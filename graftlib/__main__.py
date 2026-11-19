"""Entry point for `python -m graftlib` (used to spawn thin proxies)."""

from __future__ import annotations

import sys

from . import cli, proxy
from .config import load


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv[:1] == ["proxy"]:
        # internal: `proxy run --provider <name>` runs in the foreground
        if len(argv) >= 4 and argv[1] == "run" and argv[2] == "--provider":
            config = load()
            proxy.serve(config, config.provider(argv[3]))
            return 0
    return cli.main(argv)


if __name__ == "__main__":
    raise SystemExit(main())
