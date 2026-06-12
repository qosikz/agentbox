# Example: bake the OpenAI Codex CLI into an Andbo runtime image.
#
# An ILLUSTRATIVE example of the "baked-in agent" model: the AGENT runs inside
# the container, and Andbo is the sandbox around it. (The verified zero-cost
# path is stub.Dockerfile; Codex auth/sandbox specifics are version-dependent —
# see ../andbo.codex.yaml and ./README.md.) The API key is NEVER baked into
# the image — image layers are immutable and would leak the secret via
# `docker history`, `docker save`, or any registry push. The key is injected at
# RUNTIME via secrets.allow (CODEX_API_KEY) and redacted from logs.
#
# Build (context is this directory):
#
#   docker build -t andbo/codex:latest -f examples/agents/codex.Dockerfile examples/agents
#
# Then point your policy at it (see ../andbo.codex.yaml):
#
#   runtime.image: andbo/codex:latest
#   agent.default: codex
FROM node:20-bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates git \
	&& rm -rf /var/lib/apt/lists/*

# Install the agent CLI into the image. Pin a version in production for
# reproducibility (e.g. @openai/codex@<version>).
RUN npm install -g @openai/codex \
	&& useradd -m -u 10001 agent

# NOTE: no ENV / no ARG for the API key — credentials are injected at runtime
# by Andbo, never stored in a layer.
USER agent
WORKDIR /workspace
