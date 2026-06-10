package adapters

import "context"

type Input struct {
	Task          string
	WorkspacePath string
	Env           map[string]string
}

type Command struct {
	Executable string
	Args       []string
	Env        map[string]string
	WorkingDir string
}

type Result struct {
	Success bool
	Summary string
}

type Adapter interface {
	Name() string
	Check(ctx context.Context) error
	BuildCommand(ctx context.Context, input Input) (Command, error)
}
