# Recipe: CI dry-run for untrusted pull requests

Let an agent (or a command) plan work against a **fork PR** without running
anything or exposing secrets. The bundled composite Action defaults to
`--dry-run` and uploads the session as an artifact for review.

## Use the composite action

A worked example lives at
[`examples/github-action-andbo.yml`](../examples/github-action-andbo.yml);
the action itself is in [`.github/actions/andbo`](../.github/actions/andbo).

```yaml
name: Andbo (dry-run)
on:
  pull_request:            # includes fork PRs
permissions:
  contents: read           # no write token for untrusted PRs
jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/andbo
        with:
          task: "review the diff and suggest a fix"
          dry_run: "true"   # plan only; nothing executes
      # the session (plan + policy + audit) is uploaded as an artifact
```

## Safety rules for fork PRs

- **Keep `dry_run: true`.** A dry run plans the work and records what *would*
  happen — no agent executes, no container runs.
- **Don't expose write tokens.** Keep `permissions: contents: read`; never pass
  secrets or `open_pr: true` on untrusted PRs.
- Review the uploaded session artifact (plan, effective policy, honest
  limitations) before deciding to run anything for real on a trusted branch.

## Graduating to a real run

On a trusted branch, drop `--dry-run` and add an egress allowlist + the agent's
key via `secrets.allow`, so even a real run is contained (see
[codex-container.md](codex-container.md)).
