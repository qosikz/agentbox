package adapters

import (
	"context"
	"strings"
)

type CustomAdapter struct {
	Command string
	Args    []string
}

func (a CustomAdapter) Name() string {
	return "custom"
}

func (a CustomAdapter) Check(ctx context.Context) error {
	return nil
}

func (a CustomAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, len(a.Args))
	for _, arg := range a.Args {
		args = append(args, strings.ReplaceAll(arg, "{{ task }}", input.Task))
	}
	return Command{
		Executable: a.Command,
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
