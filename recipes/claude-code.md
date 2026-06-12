# Recipe: a safe Claude Code workflow

Run **Claude Code** as the agent inside an Andbo workspace so it can edit a
real repo and open a branch — without uncontrolled access to your host.

## Why local mode here

Claude Code authenticates via a **keychain/OAuth login**, which only works in
`--runtime local` mode (the login doesn't reach a container). Local mode forwards
a minimal env (`PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TERM`) plus
explicitly allowlisted secrets — your broader environment is not passed through.

> **Honest limitation:** `--runtime local` runs on the host and has **no network
> enforcement**. It still gives you a disposable workspace copy, secret
> redaction, a diff, and a recorded session — but for full container isolation
> use a baked-in agent (see [codex-container.md](codex-container.md)).

## Steps

```bash
# 1. Make sure you're logged into Claude Code already (keychain/OAuth).
andbo doctor            # shows whether `claude` is found

# 2. Run it on a task. The change lands in a disposable copy of your repo,
#    your test commands re-run, and --commit propagates a new branch back.
andbo run "fix the failing test in parser_test.go" \
  --agent claude \
  --runtime local --yes-unsafe \
  --commit

# 3. Review what happened — diff, commands, audit log.
andbo session show latest
```

`--yes-unsafe` acknowledges that local mode is an unsafe runtime
(non-interactive). Drop `--commit` to leave your repo untouched and only inspect
the diff in the session.

## Customize

- Point at any task; Andbo substitutes it into the agent invocation.
- Use a different built-in adapter with `--agent codex|gemini|goose|opencode`,
  or the `custom` adapter to run any CLI agent.
- Prefer full containment over OAuth convenience? Bake the agent into an image
  and run with `--runtime docker` — see [codex-container.md](codex-container.md).
