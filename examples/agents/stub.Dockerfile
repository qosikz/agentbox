# Stub-agent runtime image — proves the containerized agent path at zero cost.
#
# The stub is a baked-in "agent" that reports whether a policy-injected API key
# reached the container and makes a file change. It needs no network and no real
# key, so it exercises the full harness -> sandbox -> agent flow for free.
#
# Build (context is this directory so the COPY below resolves):
#
#   docker build -t andbo/stub-agent:latest -f examples/agents/stub.Dockerfile examples/agents
#
# Then run with the matching policy:
#
#   ANDBO_FAKE_API_KEY=dummy-not-a-real-key \
#     andbo run "prove the path" --policy examples/andbo.stub.yaml
FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates git \
	&& rm -rf /var/lib/apt/lists/*

# Bake the stub agent onto PATH. It exists ONLY in the image, never on the host
# — that is the point: it proves Andbo runs a baked-in agent the host cannot.
COPY stub-agent.sh /usr/local/bin/andbo-stub-agent
RUN chmod 0755 /usr/local/bin/andbo-stub-agent \
	&& useradd -m -u 10001 agent

USER agent
WORKDIR /workspace
