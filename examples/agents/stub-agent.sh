#!/bin/sh
# AgentBox stub agent — a zero-cost PROOF FIXTURE, not a real coding agent.
#
# It exists to prove the harness -> sandbox -> agent path end to end without any
# API key or model spend. Bake it into an image (see stub.Dockerfile) and point
# a policy at it (see ../agentbox.stub.yaml). It:
#
#   1. Confirms the policy-injected API key reached the container — printing only
#      its LENGTH, never its value, on the "present" line.
#   2. Deliberately echoes the raw key once so we can verify that AgentBox
#      REDACTS it from the recorded session (defense in depth).
#   3. Writes a file so the diff / changed-files path is exercised.
#
# It always exits 0.
set -eu

task="${1:-}"
key="${AGENTBOX_FAKE_API_KEY:-}"

echo "stub-agent: running as uid=$(id -u) user=$(id -un 2>/dev/null || echo '?')"
echo "stub-agent: task=${task}"

if [ -n "$key" ]; then
	echo "stub-agent: injected API key present (length=${#key})"
	# Defense-in-depth: AgentBox must redact this value from the saved session.
	echo "stub-agent: RAWKEY=${key} (this MUST appear redacted in the session)"
else
	echo "stub-agent: NO injected API key"
fi

printf 'stub agent ran for task: %s\n' "$task" > agentbox-stub-agent-output.txt
echo "stub-agent: wrote agentbox-stub-agent-output.txt"
