// Package config loads, defaults, validates, and serializes AgentBox policy.
//
// A policy is read from agentbox.yaml and merged over secure built-in
// defaults. The config package is intentionally dumb: it parses YAML and
// reports syntactic/semantic validity. Security-derived decisions (mandatory
// denies, deny-overrides-allow, unsafe gating) live in the policy package,
// which consumes config.Policy and produces an EffectivePolicy.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Policy is the raw, file-level policy structure. Field defaults are supplied
// by DefaultPolicy and YAML overrides are decoded on top of those defaults.
type Policy struct {
	Runtime    RuntimePolicy    `yaml:"runtime"`
	Agent      AgentPolicy      `yaml:"agent"`
	Network    NetworkPolicy    `yaml:"network"`
	Filesystem FilesystemPolicy `yaml:"filesystem"`
	Secrets    SecretsPolicy    `yaml:"secrets"`
	MCP        MCPPolicy        `yaml:"mcp"`
	Commands   CommandsPolicy   `yaml:"commands"`
	Budget     BudgetPolicy     `yaml:"budget"`
	Tests      TestsPolicy      `yaml:"tests"`
}

// RuntimePolicy controls how and where the agent executes.
type RuntimePolicy struct {
	Isolation string `yaml:"isolation"` // container | local
	Engine    string `yaml:"engine"`    // docker | podman
	Image     string `yaml:"image"`
	Cleanup   bool   `yaml:"cleanup"`
}

// AgentPolicy selects the agent adapter and configures the custom adapter.
type AgentPolicy struct {
	Default string            `yaml:"default"`
	Custom  CustomAgentConfig `yaml:"custom"`
}

// CustomAgentConfig describes a user-defined CLI agent invocation.
type CustomAgentConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

// NetworkPolicy controls outbound network access for the runtime.
type NetworkPolicy struct {
	Mode  string   `yaml:"mode"` // deny | allowlist | open
	Allow []string `yaml:"allow"`
}

// FilesystemPolicy controls which paths are readable, writable, and denied.
type FilesystemPolicy struct {
	ReadOnly []string `yaml:"readonly"`
	Write    []string `yaml:"write"`
	Deny     []string `yaml:"deny"`
}

// SecretsPolicy controls which environment secrets are exposed and redacted.
type SecretsPolicy struct {
	Mode           string   `yaml:"mode"` // explicit
	Allow          []string `yaml:"allow"`
	Deny           []string `yaml:"deny"`
	RedactPatterns []string `yaml:"redact_patterns"`
}

// MCPPolicy controls Model Context Protocol server trust.
type MCPPolicy struct {
	Mode  string   `yaml:"mode"` // allowlist | denyall | advisory
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// CommandsPolicy is an advisory allow/deny list for shell commands.
type CommandsPolicy struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// BudgetPolicy caps runtime, tokens, and spend where the adapter supports it.
type BudgetPolicy struct {
	MaxUSD            float64 `yaml:"max_usd"`
	MaxTokens         int64   `yaml:"max_tokens"`
	MaxRuntimeMinutes int     `yaml:"max_runtime_minutes"`
}

// TestsPolicy lists test commands to run after the agent completes.
type TestsPolicy struct {
	Commands []string `yaml:"commands"`
}

// DefaultPolicy returns the secure built-in defaults. These are applied
// before any agentbox.yaml is decoded on top.
func DefaultPolicy() Policy {
	return Policy{
		Runtime: RuntimePolicy{
			Isolation: "container",
			Engine:    "docker",
			Image:     "agentbox/default:latest",
			Cleanup:   true,
		},
		Agent: AgentPolicy{
			Default: "custom",
			Custom: CustomAgentConfig{
				Command: "echo",
				Args:    []string{"{{ task }}"},
			},
		},
		Network: NetworkPolicy{
			Mode: "deny",
		},
		Filesystem: FilesystemPolicy{
			ReadOnly: []string{"."},
			Write:    []string{"./src", "./tests", "./docs"},
			Deny:     []string{".env", ".env.*", "~/.ssh", "~/.aws", "~/.kube", "~/.docker"},
		},
		Secrets: SecretsPolicy{
			Mode:  "explicit",
			Allow: []string{},
			Deny:  []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY"},
		},
		MCP: MCPPolicy{
			Mode:  "allowlist",
			Allow: []string{"github-readonly", "filesystem-readonly"},
			Deny:  []string{"shell-unrestricted", "filesystem-write-all"},
		},
		Commands: CommandsPolicy{
			Allow: []string{"go test ./...", "npm test", "pnpm test", "pytest"},
			Deny:  []string{"rm -rf", "ssh", "scp", "docker run --privileged", "kubectl", "terraform apply"},
		},
		Budget: BudgetPolicy{
			MaxUSD:            5,
			MaxTokens:         1000000,
			MaxRuntimeMinutes: 30,
		},
		Tests: TestsPolicy{
			Commands: []string{"go test ./..."},
		},
	}
}

// LoadPolicy reads a policy file, decoding it on top of secure defaults.
// A missing file is not an error: secure defaults are returned. Unknown YAML
// keys are rejected to catch typos early.
func LoadPolicy(path string) (Policy, error) {
	p := DefaultPolicy()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("reading policy %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid policy %s: %w\n\nFix the YAML syntax or remove unknown keys, then run 'agentbox policy check'.", path, err)
	}
	return p, nil
}

// DefaultPolicyYAML is the commented starter policy written by `agentbox init`.
const DefaultPolicyYAML = `# AgentBox policy
# Secure defaults. Edit deliberately; unsafe options require explicit flags.
# Validate with: agentbox policy check

runtime:
  isolation: container        # container (safe) | local (unsafe)
  engine: docker              # docker | podman
  image: agentbox/default:latest
  cleanup: true

agent:
  default: custom
  custom:
    command: echo
    args:
      - "{{ task }}"

network:
  mode: deny                  # deny | allowlist | open (open is unsafe)
  allow:
    - github.com
    - pypi.org
    - registry.npmjs.org

filesystem:
  readonly:
    - .
  write:
    - ./src
    - ./tests
    - ./docs
  deny:
    - .env
    - .env.*
    - ~/.ssh
    - ~/.aws
    - ~/.kube
    - ~/.docker

secrets:
  mode: explicit
  allow:
    - GITHUB_TOKEN
  deny:
    - OPENAI_API_KEY
    - ANTHROPIC_API_KEY
    - AWS_SECRET_ACCESS_KEY
  redact_patterns:
    - "sk-[A-Za-z0-9_-]+"
    - "ghp_[A-Za-z0-9_]+"

mcp:
  mode: allowlist             # allowlist | denyall | advisory
  allow:
    - github-readonly
    - filesystem-readonly
  deny:
    - shell-unrestricted
    - filesystem-write-all

commands:
  allow:
    - go test ./...
    - npm test
    - pnpm test
    - pytest
  deny:
    - rm -rf
    - ssh
    - scp
    - docker run --privileged
    - kubectl
    - terraform apply

budget:
  max_usd: 5
  max_tokens: 1000000
  max_runtime_minutes: 30

tests:
  commands:
    - go test ./...
`

// WriteDefaultPolicy writes the commented default policy to path. It refuses
// to overwrite an existing file.
func WriteDefaultPolicy(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", path)
	}
	return os.WriteFile(path, []byte(DefaultPolicyYAML), 0o644)
}

// HashPolicyFile returns "sha256:<hex>" for the policy file, or "" if it
// cannot be read (e.g. defaults in use).
func HashPolicyFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
