# Contributing to AgentBox

Thanks for helping build AgentBox — a secure, CLI-first sandbox for AI coding
agents. The single most useful thing you can do right now is **run it on real
work and tell us what broke.** A bug report that says "it behaved differently on
my OS / engine / agent" is worth as much as a patch.

By contributing, you agree your contributions are licensed under the project's
[Apache-2.0](LICENSE) license.

## Ways to contribute

- **Use it and report back** — open a [bug report](../../issues/new/choose) with
  your `agentbox version`, OS, and container engine. Real-world coverage is what
  the test suite can't give us.
- **Add an agent adapter or runtime image** (`internal/adapters/`,
  `examples/agents/`). The `custom` adapter already runs any CLI agent; built-in
  adapters and worked images just make common ones turnkey.
- **Harden or verify enforcement** — e.g. test the network egress allowlist on
  Podman or Linux, or probe the sandbox boundaries.
- **Improve the docs** — if something confused you, a docs PR is a great first
  contribution.
- Browse [good first issues](../../issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

## Develop

Requirements: **Go 1.23+**. Docker or Podman is optional — it's only needed for
real (non-dry-run) container execution. The unit test suite is **hermetic** and
needs no daemon.

```bash
git clone https://github.com/qosikz/agentbox
cd agentbox
make build       # builds the embedded egress proxy first, then the CLI
./bin/agentbox doctor
```

The full check CI runs — all four must pass before a PR merges:

```bash
make fmt         # gofmt
make lint        # go vet
make test        # hermetic unit tests (no Docker daemon required)
make build       # cross-compiles the embedded proxy + the CLI
```

### Repository layout

| Path | What lives here |
|------|-----------------|
| `cmd/agentbox` | CLI entrypoint |
| `cmd/netproxy` | the egress-enforcement proxy (cross-compiled + embedded into the CLI) |
| `internal/cli` | commands + the `run` / `exec` pipelines |
| `internal/config`, `internal/policy` | policy schema, secure defaults, effective-policy resolution |
| `internal/runtime` | container runners, hardening flags, egress orchestration |
| `internal/netproxy` | the filtering forward proxy |
| `internal/adapters` | per-agent command builders |
| `internal/{workspace,git,session,secrets,mcpguard,mcpserve,skill}` | supporting subsystems |
| `examples/`, `demo/` | example policies/images and the recorded demos |

## Standards

- **Tests with every behavior change.** Prefer table-driven, hermetic tests.
  `make test` must never require a running Docker daemon — real-container checks
  are run manually (see the e2e steps noted in PRs).
- **Secure by default; never overclaim.** Don't weaken a default, and don't
  claim enforcement the code doesn't deliver — match the honesty of the existing
  docs. Security-relevant code paths must fail **closed**.
- Small packages, narrow interfaces, actionable errors (what failed, why, how to
  fix), standard library where practical, no unnecessary dependencies.
- Keep PRs small and focused. Update the README / `examples/` when behavior
  changes, and add a note under an `## Unreleased` heading in
  `CHANGELOG.md` for user-facing changes.

## Common contributions

### Add an agent adapter

The `custom` adapter already runs any CLI agent; a built-in adapter just makes
one turnkey. Model it on [`internal/adapters/codex.go`](internal/adapters/codex.go):

1. Add `internal/adapters/<name>.go` implementing the `Adapter` interface
   (`adapter.go`) — it builds the headless command and substitutes the task into
   `{{ task }}`.
2. Wire it up in [`registry.go`](internal/adapters/registry.go): add a
   `case "<name>":` to `Get`, and the name to `SupportedNames()` (kept sorted).
3. Add the agent to the known-agents list in
   [`internal/cli/cmd_doctor.go`](internal/cli/cmd_doctor.go) so `doctor` reports it.
4. Add a table-driven case to `internal/adapters/adapters_test.go` asserting the
   built argv (no daemon needed).
5. Appreciated: a worked `examples/agents/<name>.Dockerfile` + an
   `examples/agentbox.<name>.yaml`, and a row in the README adapter list.

The API key is injected at runtime from `secrets.allow` — **never bake it into an
image**. Keep the network on `allowlist` with the provider's domain.

### Write a security test

The security acceptance tests live in
[`internal/cli/security_test.go`](internal/cli/security_test.go). Add a case when
you touch a boundary — e.g. `.env`/`~/.ssh` excluded by default, host env not
forwarded to containers, unsafe modes gated behind `--yes-unsafe`, egress fails
closed. Tests must be **hermetic** (no Docker daemon): assert on the resolved
policy / planned runtime spec, not a live run.

### Add or update a demo

Demos under `demo/` are recordable shell scripts (see
[`demo/README.md`](demo/README.md)). Iterate fast with
`TYPE=0 HOLD=0 AGENTBOX="$PWD/bin/agentbox" demo/<name>-demo.sh`, then record with
**asciinema 3.x** (`--headless --window-size 92x30`) and render with `agg`. One
rule for security demos: **never echo a secret to live stdout** — `agentbox exec`
redacts the *saved* session, not the live stream, so stage redaction proofs via
`session show` / `logs.txt`, and verify the recording is clean
(`grep -c '<your-fake-key>' demo/<name>.cast` → `0`).

## Pull requests

1. Fork, and branch from `main`.
2. Make the change **with tests**; run `make fmt lint test build`.
3. Open a PR and fill in the template — the checklist keeps reviews fast.
4. A maintainer reviews; security-sensitive changes get extra scrutiny.

## Security

**Do not open a public issue for a vulnerability.** See [SECURITY.md](SECURITY.md)
and use GitHub's private "Report a vulnerability" flow. If your change touches
secrets, network egress, container hardening, or the MCP/skill surfaces, say so
in the PR so it gets the right review.

## Conduct

This project follows the [Contributor Covenant v2.1][cc]. Be respectful and
assume good faith. Report unacceptable behavior privately through GitHub (open a
[security advisory][adv] or contact a maintainer) rather than in a public issue.

[cc]: https://www.contributor-covenant.org/version/2/1/code_of_conduct/
[adv]: https://github.com/qosikz/agentbox/security/advisories/new
