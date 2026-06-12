# Recipe: quarantine an MCP server before you trust it

MCP (Model Context Protocol) servers can expose powerful tools to your agent —
file access, shell, network. Before you wire a new one into a harness, **scan it
statically** so you know what capabilities it asks for.

## Scan

```bash
andbo mcp scan ./path-to-mcp-server
```

- Exit code **2** if the scanner flags unsafe capabilities; **0** if clean.
- Use `--json` for machine-readable output to gate in CI.

```bash
andbo mcp scan ./server --json | jq '.findings'
```

## Gate it in CI

```yaml
- name: Quarantine new MCP server
  run: andbo mcp scan ./vendored-mcp-server   # non-zero exit fails the job
```

## What it checks (static, best-effort)

The scanner inspects the server's declared tools/capabilities for dangerous
patterns (e.g. unrestricted shell, filesystem, or network reach). It is a
**static** check — a first filter, not a guarantee. Pair it with running the
server's consumer (your agent) inside Andbo so that even a tool that slips
past the scan executes under policy: denied secrets, an egress allowlist, and a
recorded session.

> The Andbo MCP server and skill never expose unsafe flags, so a harness
> driving Andbo over MCP cannot escalate past your `andbo.yaml`.
