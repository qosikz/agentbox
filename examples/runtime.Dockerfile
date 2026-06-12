# Example Andbo runtime image.
#
# This is the image the AGENT runs inside (referenced by runtime.image in the
# policy). It is NOT the Andbo CLI image.
#
# The default policy points at the published, multi-arch, signed image
# `ghcr.io/qosikz/andbo/runtime:latest`, which `andbo run` pulls
# automatically — so you do NOT need to build anything to get started.
#
# Build your own only when you need extra toolchains (node, python, go, tests):
#
#   docker build -t my/andbo-runtime:latest -f examples/runtime.Dockerfile examples/
#
# then set `runtime.image: my/andbo-runtime:latest` in your policy.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

# Run as a non-root user; Andbo also passes --user at runtime.
RUN useradd -m -u 10001 agent
USER agent
WORKDIR /workspace
