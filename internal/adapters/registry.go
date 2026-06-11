package adapters

import (
	"fmt"
	"strings"

	"github.com/qosi/agentbox/internal/config"
)

// SupportedNames lists the adapter names Get understands, sorted.
func SupportedNames() []string {
	return []string{"aider", "claude", "codex", "custom", "gemini", "goose", "opencode"}
}

// Get returns the adapter for the given name. The custom config is only used
// when name == "custom".
func Get(name string, custom config.CustomAgentConfig) (Adapter, error) {
	switch name {
	case "custom":
		return CustomAdapter{Command: custom.Command, Args: custom.Args}, nil
	case "claude":
		return ClaudeAdapter{}, nil
	case "aider":
		return AiderAdapter{}, nil
	case "codex":
		return CodexAdapter{}, nil
	case "gemini":
		return GeminiAdapter{}, nil
	case "opencode":
		return OpenCodeAdapter{}, nil
	case "goose":
		return GooseAdapter{}, nil
	case "":
		return nil, fmt.Errorf("no agent selected: set an agent (e.g. --agent custom) or agent.default in your policy; supported: %s", strings.Join(SupportedNames(), ", "))
	default:
		return nil, fmt.Errorf("unknown agent %q: supported agents are %s", name, strings.Join(SupportedNames(), ", "))
	}
}
