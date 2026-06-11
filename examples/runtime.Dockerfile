# Example AgentBox runtime image.
#
# This is the image the AGENT runs inside (referenced by runtime.image in the
# policy, default: agentbox/default:latest). It is NOT the AgentBox CLI image.
# Build it before doing real (non-dry-run) container runs:
#
#   docker build -t agentbox/default:latest -f examples/runtime.Dockerfile examples/
#
# Add whatever toolchains your agent and tests need (node, python, go, etc.).
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates git curl \
    && rm -rf /var/lib/apt/lists/*

# Run as a non-root user; AgentBox also passes --user at runtime.
RUN useradd -m -u 10001 agent
USER agent
WORKDIR /workspace
