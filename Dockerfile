FROM golang:1.23-bookworm AS build
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/agentbox ./cmd/agentbox

FROM debian:bookworm-slim
# agentbox shells out to git at runtime (diffs, branches, clones) and needs
# CA certificates for HTTPS (e.g. cloning remotes, gh API calls).
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
# Security: run as a dedicated non-root user.
RUN useradd -m -u 10001 agentbox
USER agentbox
COPY --from=build /out/agentbox /usr/local/bin/agentbox
ENTRYPOINT ["agentbox"]
