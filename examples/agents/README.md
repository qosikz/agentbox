# Running a coding agent *inside* the AgentBox sandbox

AgentBox is a sandbox, not an agent. To run a real coding agent fully
containerized, **bake the agent's CLI into a runtime image** and let AgentBox
run it under policy. The flow is:

```
harness / you  ->  agentbox run  ->  container (your image)  ->  agent CLI
                                         ^ secret injected here, redacted from logs
```

## The model

1. **Bake the agent CLI into an image** (this directory has worked examples).
   The agent binary lives in the image, not on your host.
2. **Point the policy at the image and agent** (`runtime.image`, `agent.default`).
3. **Inject credentials at runtime** via `secrets.allow` — the value is read
   from your host environment, passed into the container, and **redacted** from
   the recorded session.

AgentBox preflights the agent by probing the **image** (not the host PATH), so a
baked-in agent that does not exist on your machine still runs.

## Security: the API key is never in the image

Do **not** bake an API key into an image. Image layers are immutable and
persistent — a baked-in secret leaks via `docker history`, `docker save`, and
any registry push. None of the Dockerfiles here contain a key, an `ENV`, or an
`ARG` for one.

Instead, AgentBox injects the key at **runtime** and only when policy allows it:

- `secrets.allow` lists the env var names passed into the container.
- `secrets.deny` wins over allow (deny-overrides-allow). The secure defaults
  deny `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, and `AWS_SECRET_ACCESS_KEY`, so to
  pass one of those you must remove it from `deny` deliberately.
- Allowed and denied secret values are both **redacted** from logs, diffs, and
  session metadata.

The MCP server and the cross-harness skill never expose unsafe flags, so a
harness driving AgentBox cannot escalate past your policy.

## Network: the enforced allowlist

A real agent reaches its model API through the **enforced** allowlist: set
`network.mode: allowlist` with your provider's API domains and the agent can
call those and nothing else. Enforcement is structural, not advisory — the
agent container's only interface is a per-run isolated network whose sole
exit is an egress proxy restricted to your domains; traffic that ignores the
proxy (and external DNS) fails closed. Every allowed/denied connection is
recorded in the session. No unsafe confirmation is needed.

Caveats (honest): HTTP(S) only — SSH/raw-TCP cannot leave the sandbox at all;
local (`--runtime local`) runs have no network enforcement; an allowlisted
domain is a permitted channel by definition, so keep the list minimal.

## Build the images

```bash
# Zero-cost proof fixture (no key, no network):
docker build -t agentbox/stub-agent:latest -f stub.Dockerfile .

# Real example — OpenAI Codex:
docker build -t agentbox/codex:latest -f codex.Dockerfile .
```

(Run these from `examples/agents/`, or pass the full `-f examples/agents/...`
path with `examples/agents` as the build context.)

## Prove the path for free (stub agent)

The stub agent confirms the key was injected and makes a file change — no API
call, no spend:

```bash
docker build -t agentbox/stub-agent:latest -f stub.Dockerfile .

AGENTBOX_FAKE_API_KEY=dummy-not-a-real-key-0xCAFE \
  agentbox run "prove the path" --policy ../agentbox.stub.yaml
```

You should see the stub run as uid `10001` inside the container, report the
injected key's length, and write `agentbox-stub-agent-output.txt`. Open the
saved session JSON and confirm the dummy key value appears only as
`[REDACTED:AGENTBOX_FAKE_API_KEY]`.

## Run a real agent (Codex) — illustrative

Codex's auth and its own sandbox are version-dependent, so treat this as a
starting point, not a verified turnkey run (the **stub** above is the verified
path). `codex exec` reads `CODEX_API_KEY` for a single non-interactive run.

```bash
export CODEX_API_KEY=sk-...
agentbox run "add a test for parseConfig" \
  --policy ../agentbox.codex.yaml          # no unsafe flag: allowlist is enforced
```

Codex ships its own Landlock/Seatbelt sandbox that can conflict with the
container; if so, switch `agentbox.codex.yaml` to the commented `custom` block
that passes `--dangerously-bypass-approvals-and-sandbox` (AgentBox's container
is then the enforcement boundary).

## Adapting for other agents

The same pattern works for any CLI agent. Build an image with the agent
installed, then either use a built-in adapter (`codex`, `gemini`, `goose`,
`opencode`, `claude`) or the `custom` adapter:

| Agent   | Image base    | Install                          | Auth env (inject via `secrets.allow`) |
|---------|---------------|----------------------------------|---------------------------------------|
| Codex   | `node:20`     | `npm i -g @openai/codex`         | `CODEX_API_KEY` (for `codex exec`)     |
| Gemini  | `node:20`     | `npm i -g @google/gemini-cli`    | `GEMINI_API_KEY`                       |
| Goose   | `debian`      | `brew install block-goose-cli` / official installer | provider key (e.g. `OPENAI_API_KEY`) |
| Custom  | anything      | your binary on `PATH`            | whatever your tool reads               |

Auth-env names are version- and provider-specific — confirm against your
agent's current docs. Only `CODEX_API_KEY`/`GEMINI_API_KEY` etc. need to be in
`secrets.allow`; the secure defaults already deny `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, and `AWS_SECRET_ACCESS_KEY`, so allow those only if your
agent specifically reads them (and remove them from `deny`).

For the `custom` adapter, set `agent.custom.command` to the baked-in binary name
(as in `agentbox.stub.yaml`). AgentBox substitutes the task into
`{{ task }}` placeholders.

> Note on Claude Code: it authenticates via OAuth, whose token lives on the
> host and does not reach a container. Use Claude Code with `--runtime local`
> (unsafe), or an API-key agent for the containerized path.
