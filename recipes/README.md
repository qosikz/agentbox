# Andbo recipes

Short, copy-pasteable guides for common jobs. Each is built from features in the
[main README](../README.md) — no new concepts.

| Recipe | What it does |
|--------|--------------|
| [Safe Claude Code workflow](claude-code.md) | Run Claude Code as the agent (local OAuth), land changes on a branch, recorded. |
| [Containerized Codex agent](codex-container.md) | Bake an agent into an image; inject the key at runtime; egress-allowlisted. |
| [MCP server quarantine](mcp-quarantine.md) | Statically scan an MCP server for dangerous capabilities before trusting it. |
| [CI dry-run for untrusted PRs](github-actions-untrusted-pr.md) | Plan against a fork PR with no execution and no write tokens. |

For an egress allowlist scoped to your model API (the v0.4.0 headline), see the
[main README](../README.md#recipes) and
[`examples/andbo.codex.yaml`](../examples/andbo.codex.yaml).
