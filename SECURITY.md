# Security Policy

## Supported Versions

AgentBox is pre-release. Security reports are accepted for the main branch.

## Reporting a Vulnerability

Please report security issues privately to the maintainers.

Include:

- Description
- Impact
- Reproduction steps
- Affected version/commit
- Suggested mitigation if known

## Security Principles

AgentBox uses secure defaults:

- No host secrets by default.
- No Docker socket by default.
- No privileged containers by default.
- Sensitive host paths denied by default.
- Unsafe modes require explicit opt-in.
