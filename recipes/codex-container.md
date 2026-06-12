# Recipe: a containerized Codex agent

Run a coding agent **fully inside the sandbox** by baking its CLI into a runtime
image. The agent binary lives in the image, not on your host; its API key is
injected at runtime (never baked in) and redacted from logs.

## Prove the path for free first (no key, no spend)

The bundled **stub agent** exercises the whole harness → sandbox → agent flow at
zero cost:

```bash
docker build -t andbo/stub-agent:latest -f examples/agents/stub.Dockerfile examples/agents
ANDBO_FAKE_API_KEY=dummy-not-a-real-key \
  andbo run "prove the path" --policy examples/andbo.stub.yaml
andbo session show latest   # the dummy key shows only as [REDACTED:...]
```

## Run Codex containerized

```bash
# 1. Build an image with the agent CLI baked in.
docker build -t andbo/codex:latest -f examples/agents/codex.Dockerfile examples/agents

# 2. Inject the key at runtime (NEVER baked into the image) and run.
export CODEX_API_KEY=sk-...
andbo run "add a test for parseConfig" --policy examples/andbo.codex.yaml
```

The policy [`examples/andbo.codex.yaml`](../examples/andbo.codex.yaml) uses
`network.mode: allowlist` with `api.openai.com`, so a real run needs **no**
`--yes-unsafe` — the agent reaches its model API and nothing else.

> Codex's auth/sandbox specifics are version-dependent; `codex exec` reads
> `CODEX_API_KEY`. If Codex's own sandbox conflicts with the container, use the
> `custom` adapter with the appropriate bypass flag.

## Why the key is safe

- **Never in the image.** Image layers are immutable and would leak a baked-in
  secret via `docker history` / `docker save` / a registry push. Andbo reads
  the key from your host env and injects it only when `secrets.allow` lists it
  (and `secrets.deny` does not), redacting the value from logs, diffs, and the
  saved session.
- **Network is contained.** Allowlist the provider's domain(s); everything else
  fails closed. Need a non-standard port (internal service)? See
  [`network.ports`](../SECURITY.md) — it widens egress for *that* host:port only.
