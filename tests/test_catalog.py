"""The /model picker must list only the models the active graft provider can serve."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from graftlib import catalog, codex  # noqa: E402
from graftlib.config import Config, Provider  # noqa: E402


def provider(name: str, models: list[str], **kwargs) -> Provider:
    return Provider(
        name=name,
        label=kwargs.pop("label", name),
        transport=kwargs.pop("transport", "direct"),
        upstream=kwargs.pop("upstream", "https://example.invalid/"),
        auth=kwargs.pop("auth", "api-key"),
        models=models,
        **kwargs,
    )


def config_for(*providers: Provider) -> Config:
    return Config(
        path=Path("/tmp/graft-test.toml"),
        codex_home=Path("/tmp/codex-home"),
        proxy_port=8787,
        profile_prefix="graft-",
        providers={item.name: item for item in providers},
        raw={},
    )


class CatalogTest(unittest.TestCase):
    def test_grok_catalog_contains_only_its_models(self) -> None:
        grok = provider("grok", ["grok-4.7", "grok-4.6"], transport="thin", context_window=256000)
        built = catalog.build(config_for(grok), grok)
        slugs = [entry["slug"] for entry in built["models"]]
        self.assertEqual(slugs, ["grok-4.7", "grok-4.6"])
        self.assertFalse(any(slug.startswith("gpt-") for slug in slugs))

    def test_providers_do_not_share_a_catalog(self) -> None:
        grok = provider("grok", ["grok-4.7"], transport="thin")
        deepseek = provider("deepseek", ["deepseek-flash"])
        config = config_for(grok, deepseek)
        self.assertNotEqual(catalog.catalog_path(config, grok), catalog.catalog_path(config, deepseek))
        self.assertEqual(
            [entry["slug"] for entry in catalog.build(config, deepseek)["models"]],
            ["deepseek-flash"],
        )

    def test_profile_points_codex_at_the_catalog(self) -> None:
        grok = provider("grok", ["grok-4.7"], transport="thin", port=8787)
        config = config_for(grok)
        body = codex.profile_body(config, grok)
        self.assertIn('model_catalog_json = "/tmp/codex-home/graft-grok-models.json"', body)
        self.assertNotIn("gpt-", body)

    def test_stray_config_keys_do_not_enter_the_catalog(self) -> None:
        grok = provider("grok", ["grok-4.7"], extra={"notes": "do not leak", "shell_type": "shell_command"})
        entry = catalog.entry_for(grok, "grok-4.7")
        self.assertNotIn("notes", entry)
        self.assertEqual(entry["shell_type"], "shell_command")

    def test_every_entry_has_the_required_prompt(self) -> None:
        grok = provider("grok", ["grok-4.7"])
        entry = catalog.entry_for(grok, "grok-4.7")
        template = entry["model_messages"]["instructions_template"]
        self.assertTrue(template.startswith("You are Codex"))
        self.assertGreater(len(template), 1000)
