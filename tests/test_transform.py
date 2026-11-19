"""Unit tests for the thin transport rewrites.

Run: python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from graftlib.transform import GROK_TOOL_TYPES, rewrite_for_grok  # noqa: E402


def base_request() -> dict:
    return {
        "model": "grok-4.7",
        "stream": True,
        "store": False,
        "include": ["reasoning.encrypted_content"],
        "reasoning": {"effort": "high", "summary": "auto"},
        "input": [{"type": "message", "role": "user", "content": "hi"}],
        "tools": [],
        "tool_choice": "auto",
        "parallel_tool_calls": True,
    }


class RewriteForGrokTest(unittest.TestCase):
    def test_namespace_tools_are_dropped(self) -> None:
        body = base_request()
        body["tools"] = [
            {"type": "function", "name": "exec_command"},
            {"type": "namespace", "name": "multi_agent_v1", "tools": [{"type": "function", "name": "spawn"}]},
        ]
        result = rewrite_for_grok(body)
        self.assertEqual([t["name"] for t in result.body["tools"]], ["exec_command"])
        self.assertIn("r1:drop-namespace:multi_agent_v1", result.notes)

    def test_web_search_sibling_fields_are_stripped(self) -> None:
        body = base_request()
        body["tools"] = [{"type": "web_search", "external_web_access": False}]
        result = rewrite_for_grok(body)
        self.assertEqual(result.body["tools"], [{"type": "web_search"}])
        self.assertTrue(any(note.startswith("r2:") for note in result.notes))

    def test_web_search_without_extras_is_untouched(self) -> None:
        body = base_request()
        body["tools"] = [{"type": "web_search"}]
        result = rewrite_for_grok(body)
        self.assertEqual(result.body["tools"], [{"type": "web_search"}])
        self.assertEqual(result.notes, [])

    def test_reasoning_items_are_dropped_from_input(self) -> None:
        body = base_request()
        body["input"] = [
            {"type": "message", "role": "developer", "content": "x"},
            {"type": "reasoning", "id": "rs_1", "summary": [], "encrypted_content": "blob"},
            {"type": "function_call", "name": "exec_command", "arguments": "{}", "call_id": "c1"},
            {"type": "function_call_output", "call_id": "c1", "output": "ok"},
        ]
        result = rewrite_for_grok(body)
        types = [item["type"] for item in result.body["input"]]
        self.assertEqual(types, ["message", "function_call", "function_call_output"])
        self.assertIn("r3:drop-reasoning-items:1", result.notes)

    def test_include_is_preserved(self) -> None:
        result = rewrite_for_grok(base_request())
        self.assertEqual(result.body["include"], ["reasoning.encrypted_content"])

    def test_unknown_tool_types_are_dropped(self) -> None:
        body = base_request()
        body["tools"] = [
            {"type": "function", "name": "ok"},
            {"type": "custom", "name": "apply_patch"},
        ]
        result = rewrite_for_grok(body)
        self.assertEqual(result.body["tools"], [{"type": "function", "name": "ok"}])
        self.assertTrue(any(note.startswith("drop-unsupported-tool:custom") for note in result.notes))

    def test_input_is_not_mutated(self) -> None:
        body = base_request()
        original = repr(body)
        rewrite_for_grok(body)
        self.assertEqual(repr(body), original)

    def test_allowed_tool_types_cover_the_upstream_whitelist(self) -> None:
        for tool_type in ("function", "web_search", "x_search", "mcp", "shell"):
            self.assertIn(tool_type, GROK_TOOL_TYPES)


if __name__ == "__main__":
    unittest.main()
