"""graft - run codex against non-OpenAI LLM subscriptions.

Layout:
  graft codex -m grok-4.7        thin proxy, borrows the Grok CLI subscription
  graft codex -m deepseek-flash  direct, plain config layering, no process
"""

from .cli import main

__all__ = ["main"]
