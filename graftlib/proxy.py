"""Thin proxy: byte rewrites + credential injection, no protocol translation.

One process per provider, listening on 127.0.0.1 only. It re-reads the upstream
credential on every request so a CLI token refresh or a `grok login` account
switch is picked up without restarting anything.
"""

from __future__ import annotations

import gzip
import json
import os
import shutil
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

from . import creds
from .config import Config, Provider
from .transform import TRANSPORTS

UPSTREAM_TIMEOUT = 600
RETRY_STATUS = {401, 426}


def log(message: str) -> None:
    stamp = time.strftime("%H:%M:%S")
    print(f"{stamp} {message}", flush=True)


def decode_body(raw: bytes, encoding: str) -> bytes:
    """codex may zstd/gzip its request body; unwrap before rewriting."""
    enc = (encoding or "").lower()
    if not enc:
        return raw
    if "gzip" in enc:
        return gzip.decompress(raw)
    if "zstd" in enc:
        binary = shutil.which("zstd")
        if binary:
            out = subprocess.run([binary, "-d", "-c"], input=raw, capture_output=True)
            if out.returncode == 0:
                return out.stdout
        raise ValueError("zstd-compressed request body and no zstd binary available")
    return raw


class Upstream:
    """Credential + endpoint resolution for one provider."""

    def __init__(self, provider: Provider):
        self.provider = provider
        self.transport = TRANSPORTS.get(provider.name) or (TRANSPORTS["grok"] if provider.transport == "thin" else None)

    def headers(self) -> dict[str, str]:
        base = {"content-type": "application/json", "accept": "text/event-stream"}
        if self.provider.auth == "borrow":
            session = creds.grok_session()
            base["authorization"] = "Bearer " + session["key"]
            base["x-xai-token-auth"] = "xai-grok-cli"
            # the proxy 426s without a client version, so follow the CLI
            base["x-grok-client-version"] = creds.grok_client_version()
        elif self.provider.auth == "api-key":
            base["authorization"] = "Bearer " + creds.api_key(self.provider)
        return base

    def url(self, path: str) -> str:
        """Build the upstream URL, dropping a base path the client repeated.

        codex always calls `/v1/responses`; when the upstream base already ends
        in `/v1` the duplicated segment would 404.
        """
        base = urlparse(self.provider.upstream)
        base_path = base.path.rstrip("/")
        request_path = "/" + path.lstrip("/").split("?")[0]
        query = ("?" + path.split("?", 1)[1]) if "?" in path else ""
        if base_path and request_path.startswith(base_path + "/"):
            request_path = request_path[len(base_path):]
        return f"{base.scheme}://{base.netloc}{base_path}{request_path}{query}"


def make_handler(provider: Provider) -> type[BaseHTTPRequestHandler]:
    upstream = Upstream(provider)

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        server_version = "graft-thin"

        def log_message(self, fmt: str, *args) -> None:  # noqa: A003
            return  # silence default per-request noise; we log our own

        def handle_one_request(self) -> None:
            # codex aborts requests on reconnect; do not dump tracebacks
            try:
                super().handle_one_request()
            except (BrokenPipeError, ConnectionResetError):
                self.close_connection = True

        # ---- helpers -----------------------------------------------------
        def _json(self, status: int, payload: dict) -> None:
            data = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _read_body(self) -> bytes:
            length = int(self.headers.get("content-length") or 0)
            raw = self.rfile.read(length) if length else b""
            return decode_body(raw, self.headers.get("content-encoding", ""))

        # ---- GET ---------------------------------------------------------
        def do_GET(self) -> None:  # noqa: N802
            if self.path.rstrip("/") in ("/graft/health", "/health"):
                return self._json(200, {"ok": True, "provider": provider.name, "transport": "thin"})
            return self._forward("GET", b"")

        # ---- POST --------------------------------------------------------
        def do_POST(self) -> None:  # noqa: N802
            try:
                raw = self._read_body()
            except (BrokenPipeError, ConnectionResetError):
                self.close_connection = True
                return
            except Exception as exc:  # noqa: BLE001
                return self._json(400, {"error": f"graft could not decode request body: {exc}"})

            if self.path.split("?")[0].rstrip("/").endswith("/responses"):
                try:
                    body = json.loads(raw)
                except json.JSONDecodeError as exc:
                    return self._json(400, {"error": f"graft could not parse json: {exc}"})
                result = upstream.transport(body) if upstream.transport else None
                if result:
                    body, notes = result.body, result.notes
                    if notes:
                        log(f"{provider.name} rewrite {', '.join(notes)}")
                    raw = json.dumps(body).encode()
            return self._forward("POST", raw)

        # ---- forwarding ---------------------------------------------------
        def _forward(self, method: str, raw: bytes, attempt: int = 0) -> None:  # noqa: C901
            path = self.path.lstrip("/")
            url = upstream.url(self.path)
            timeout = UPSTREAM_TIMEOUT
            request = urllib.request.Request(url, data=raw if method != "GET" else None, method=method)
            for key, value in upstream.headers().items():
                request.add_header(key, value)
            request.add_header("accept-encoding", "identity")

            context = ssl.create_default_context()
            try:
                response = urllib.request.urlopen(request, context=context, timeout=timeout)
            except urllib.error.HTTPError as exc:  # noqa: PERF203
                payload = exc.read()
                if exc.code in RETRY_STATUS and attempt == 0:
                    log(f"{provider.name} upstream {exc.code}, retrying after credential reload")
                    return self._forward(method, raw, attempt=1)
                log(f"{provider.name} upstream {exc.code} for {path}: {payload[:200]!r}")
                self.send_response(exc.code)
                self.send_header("content-type", exc.headers.get("content-type", "application/json"))
                self.send_header("content-length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
                return
            except urllib.error.URLError as exc:
                log(f"{provider.name} upstream unreachable: {exc}")
                return self._json(502, {"error": f"graft upstream unreachable: {exc}"})

            self.send_response(response.status)
            self.send_header("content-type", response.headers.get("content-type", "text/event-stream"))
            self.send_header("cache-control", "no-store")
            self.send_header("transfer-encoding", "chunked")
            self.end_headers()
            try:
                while True:
                    chunk = response.read(2048)
                    if not chunk:
                        break
                    self.wfile.write(b"%x\r\n" % len(chunk) + chunk + b"\r\n")
                    self.wfile.flush()
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                log(f"{provider.name} client disconnected mid-stream")

    return Handler


def serve(config: Config, provider: Provider) -> None:
    handler = make_handler(provider)
    server = ThreadingHTTPServer(("127.0.0.1", provider.port), handler)
    server.daemon_threads = True
    log(f"graft thin proxy for {provider.name} listening on 127.0.0.1:{provider.port} -> {provider.upstream}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log("graft thin proxy stopping")


def _run_dir() -> Path:
    return Path(os.environ.get("GRAFT_RUN_DIR") or (Path(os.path.expanduser("~")) / ".config/graft/run"))


def pid_file(provider: Provider) -> Path:
    return _run_dir() / f"{provider.name}.pid"


def log_file(provider: Provider) -> Path:
    return _run_dir() / f"{provider.name}.log"


def healthy(provider: Provider, timeout: float = 0.4) -> bool:
    try:
        url = f"http://127.0.0.1:{provider.port}/graft/health"
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return json.loads(response.read()).get("provider") == provider.name
    except Exception:  # noqa: BLE001
        return False


def ensure(config: Config, provider: Provider, quiet: bool = False) -> None:
    """Start the proxy in the background if it is not already healthy."""
    if not provider.is_thin:
        return
    if healthy(provider):
        return
    _run_dir().mkdir(parents=True, exist_ok=True)
    log_path = log_file(provider)
    env = dict(os.environ)
    env["GRAFT_CONFIG"] = str(config.path)
    package_root = str(Path(__file__).resolve().parent.parent)
    env["PYTHONPATH"] = package_root + (":" + env["PYTHONPATH"] if env.get("PYTHONPATH") else "")
    with open(log_path, "ab") as handle:
        proc = subprocess.Popen(
            [sys.executable, "-m", "graftlib", "proxy", "run", "--provider", provider.name],
            stdout=handle, stderr=handle, env=env, start_new_session=True,
        )
    pid_file(provider).write_text(str(proc.pid))
    deadline = time.time() + 8
    while time.time() < deadline:
        if healthy(provider):
            if not quiet:
                log(f"graft thin proxy for {provider.name} started on port {provider.port} (pid {proc.pid})")
            return
        if proc.poll() is not None:
            break
        time.sleep(0.1)
    raise SystemExit(f"graft proxy for '{provider.name}' failed to start; see {log_path}")


def stop(provider: Provider) -> bool:
    path = pid_file(provider)
    if not path.exists():
        return False
    pid = int(path.read_text().strip() or 0)
    try:
        if pid:
            os.kill(pid, 15)
    except ProcessLookupError:
        pass
    path.unlink(missing_ok=True)
    return True
