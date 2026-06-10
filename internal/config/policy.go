package config

import (
	"errors"
	"os"
)

type Policy struct {
	Runtime   RuntimePolicy
	Network   NetworkPolicy
	Filesystem FilesystemPolicy
	Secrets   SecretsPolicy
}

type RuntimePolicy struct {
	Isolation string
	Engine    string
	Image     string
	Cleanup   bool
}

type NetworkPolicy struct {
	Mode  string
	Allow []string
}

type FilesystemPolicy struct {
	ReadOnly []string
	Write    []string
	Deny     []string
}

type SecretsPolicy struct {
	Mode  string
	Allow []string
	Deny  []string
}

func DefaultPolicy() Policy {
	return Policy{
		Runtime: RuntimePolicy{
			Isolation: "container",
			Engine:    "docker",
			Image:     "agentbox/default:latest",
			Cleanup:   true,
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
			Mode: "explicit",
			Allow: []string{},
			Deny:  []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY"},
		},
	}
}

func LoadPolicy(path string) (Policy, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return DefaultPolicy(), nil
	}
	// TODO: implement YAML parsing in Phase 2.
	return DefaultPolicy(), nil
}

func WriteDefaultPolicy(path string) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("agentbox.yaml already exists; refusing to overwrite")
	}
	content := `runtime:
  isolation: container
  engine: docker
  image: agentbox/default:latest
  cleanup: true

network:
  mode: deny

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
  allow: []
  deny:
    - OPENAI_API_KEY
    - ANTHROPIC_API_KEY
    - AWS_SECRET_ACCESS_KEY
`
	return os.WriteFile(path, []byte(content), 0644)
}
