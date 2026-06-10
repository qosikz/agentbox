FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY . .
RUN go build -o /out/agentbox ./cmd/agentbox

FROM debian:bookworm-slim
RUN useradd -m -u 10001 agentbox
USER agentbox
COPY --from=build /out/agentbox /usr/local/bin/agentbox
ENTRYPOINT ["agentbox"]
