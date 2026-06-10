# AgentBox

Safe workspaces for AI coding agents.

AgentBox lets developers run AI coding agents against real repositories without exposing local secrets, unrestricted network access, or sensitive host files.

```bash
agentbox run "fix failing tests"
```

## Status

Early scaffold. Use the build pack docs and Claude Code prompts to implement MVP.

## Development

```bash
make fmt
make test
make build
```

## Basic commands

```bash
go run ./cmd/agentbox version
go run ./cmd/agentbox init
go run ./cmd/agentbox doctor
```

## Documentation

See `docs/`.
