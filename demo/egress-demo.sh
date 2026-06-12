#!/usr/bin/env bash
#
# AgentBox egress-enforcement demo — recordable with asciinema.
#
#   asciinema rec egress.cast --idle-time-limit 1.5 --cols 92 --rows 28 \
#     -c demo/egress-demo.sh
#
# The story (≈60s): give the sandbox a network allowlist of ONE domain, then
# watch a command reach that domain, get blocked from everything else (fail
# closed), find DNS dead (no exfil side channel), and see it all in the audit
# trail. Everything is real — real Docker, real egress proxy, no mocks.
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
cat > agentbox.yaml <<'YAML'
runtime:
  isolation: container
  image: agentbox/stub-agent:latest
network:
  mode: allowlist          # ENFORCED, not advisory
  allow:
    - github.com           # the ONLY domain this sandbox may reach
YAML

clear
pc "AgentBox: your agent reaches exactly what you allow — and nothing else."
pc "The policy allows one domain:"
pe "cat agentbox.yaml"

pc "github.com is allowed — the sandbox reaches it:"
pe "agentbox exec 'git ls-remote https://github.com/git/git.git HEAD'"

pc "Anything else: DNS is dead — no resolution side channel to exfil through:"
pe "agentbox exec 'getent hosts gitlab.com || echo \"  → DNS blocked (exit \$?)\"'" || true

pc "And the connection itself fails closed at the proxy:"
pe "agentbox exec 'git ls-remote https://gitlab.com/gitlab-org/gitlab.git HEAD'" || true

pc "Every attempt is in the audit trail:"
pe "agentbox session show latest | grep -E 'exit 128|BLOCKED egress'"

printf '\n%sEnforced egress: allowlist your model API, deny the rest. github.com/qosikz/agentbox%s\n' "$DIM" "$RST"
sleep 2
