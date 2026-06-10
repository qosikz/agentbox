package runtime

import "context"

type RuntimeSpec struct {
	Engine      string
	Image       string
	NetworkMode string
	Workdir     string
	Env         map[string]string
}

type CommandSpec struct {
	Executable string
	Args       []string
	Env        map[string]string
	WorkingDir string
}

type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type Runner interface {
	Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error)
}
