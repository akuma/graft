"""Request rewrites applied by thin transports.

Each rule exists because a real upstream rejected codex's request. Keep them
narrow, named and testable: a transport is only allowed to change bytes that the
upstream provably refuses, never semantics.

Verified against cli-chat-proxy.grok.com (2026-11):
  R1 namespace tools      -> 422 "unknown variant `namespace`"
  R2 web_search extras    -> 400 "Argument not supported: external_web_access"
  R3 echoed reasoning     -> 400 "Could not decode the compaction blob"
"""

from __future__ import annotations

import copy
from dataclasses import dataclass, field

# Tool types the Grok responses proxy accepts (from its own 422 whitelist).
GROK_TOOL_TYPES = {
    "function", "web_search", "x_search", "image_generation",
    "collections_search", "file_search", "code_execution",
    "code_interpreter", "mcp", "shell", "tool_search",
}


@dataclass
class RewriteResult:
    body: dict
    notes: list[str] = field(default_factory=list)


def drop_namespaces(tools: list[dict], notes: list[str]) -> list[dict]:
    """R1: codex wraps sub-agent tools in `type: namespace`; the upstream 422s.

    Dropping beats flattening: a flattened name would come back as a tool call
    codex cannot dispatch, so the model must simply not see them.
    """
    kept = []
    for tool in tools:
        if tool.get("type") == "namespace":
            notes.append(f"r1:drop-namespace:{tool.get('name')}")
            continue
        kept.append(tool)
    return kept


def strip_web_search_fields(tool: dict, notes: list[str]) -> dict:
    """R2: only `{"type": "web_search"}` is accepted; any sibling key is a 400."""
    if len(tool) > 1:
        notes.append("r2:strip-web-search-fields:" + ",".join(sorted(k for k in tool if k != "type")))
        return {"type": "web_search"}
    return tool


def drop_reasoning_items(items: list[dict], notes: list[str]) -> list[dict]:
    """R3: codex echoes `reasoning` items with its own encrypted blob.

    The blob is signed by the upstream; re-serialising it (even byte identical
    ids) yields "Could not decode the compaction blob". Reasoning continuity
    across turns is the price of admission here.
    """
    kept = [item for item in items if item.get("type") != "reasoning"]
    dropped = len(items) - len(kept)
    if dropped:
        notes.append(f"r3:drop-reasoning-items:{dropped}")
    return kept


def drop_unsupported_tools(tools: list[dict], allowed: set[str], notes: list[str]) -> list[dict]:
    kept = []
    for tool in tools:
        if tool.get("type") in allowed:
            kept.append(tool)
        else:
            notes.append(f"drop-unsupported-tool:{tool.get('type')}:{tool.get('name')}")
    return kept


def rewrite_for_grok(body: dict) -> RewriteResult:
    """Apply R1..R3 to a Responses API request body. Input is not mutated."""
    out = copy.deepcopy(body)
    notes: list[str] = []

    tools = out.get("tools")
    if isinstance(tools, list):
        tools = drop_namespaces(tools, notes)
        tools = [strip_web_search_fields(t, notes) if t.get("type") == "web_search" else t for t in tools]
        out["tools"] = drop_unsupported_tools(tools, GROK_TOOL_TYPES, notes)

    items = out.get("input")
    if isinstance(items, list):
        out["input"] = drop_reasoning_items(items, notes)

    # `include: ["reasoning.encrypted_content"]` is tolerated (verified 200);
    # only the echoed items are fatal, so leave `include` untouched.
    return RewriteResult(body=out, notes=notes)


TRANSPORTS = {
    # transport name -> callable(body) -> RewriteResult
    "grok": rewrite_for_grok,
}
