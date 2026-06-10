package cli

import (
	"fmt"
	"strings"
)

// runOptions holds parsed flags/positionals for `agentbox run`.
type runOptions struct {
	task              string
	repo              string
	agent             string
	policy            string
	network           string
	runtime           string
	write             []string
	dryRun            bool
	commit            bool
	openPR            bool
	unsafe            bool
	allowHostHome     bool
	allowDockerSocket bool
	yesUnsafe         bool
	json              bool
}

var runValueFlags = map[string]bool{
	"task": true, "repo": true, "agent": true, "policy": true,
	"network": true, "runtime": true, "write": true,
}

// parseRunFlags parses `run` arguments: a positional task or repo plus flags.
func parseRunFlags(args []string) (runOptions, error) {
	o := runOptions{policy: "agentbox.yaml"}
	var positionals []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positionals = append(positionals, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		val, inline := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			val, name, inline = name[eq+1:], name[:eq], true
		}
		if runValueFlags[name] && !inline {
			if i+1 >= len(args) {
				return o, fmt.Errorf("flag --%s requires a value", name)
			}
			i++
			val = args[i]
		}
		switch name {
		case "task":
			o.task = val
		case "repo":
			o.repo = val
		case "agent":
			o.agent = val
		case "policy":
			o.policy = val
		case "network":
			o.network = val
		case "runtime":
			o.runtime = val
		case "write":
			o.write = append(o.write, val)
		case "dry-run":
			o.dryRun = true
		case "commit":
			o.commit = true
		case "open-pr":
			o.openPR = true
		case "unsafe":
			o.unsafe = true
		case "allow-host-home":
			o.allowHostHome = true
		case "allow-docker-socket":
			o.allowDockerSocket = true
		case "yes-unsafe":
			o.yesUnsafe = true
		case "json":
			o.json = true
		default:
			return o, fmt.Errorf("unknown flag: --%s", name)
		}
	}

	for _, p := range positionals {
		switch {
		case looksLikeRepo(p) && o.repo == "":
			o.repo = p
		case o.task == "":
			o.task = p
		default:
			return o, fmt.Errorf("unexpected argument: %q", p)
		}
	}
	return o, nil
}

// looksLikeRepo reports whether s is a git repository reference rather than a
// task string.
func looksLikeRepo(s string) bool {
	if strings.HasPrefix(s, "git@") || strings.Contains(s, "://") {
		return true
	}
	parts := strings.SplitN(s, "/", 2)
	return len(parts) == 2 && strings.Contains(parts[0], ".") && parts[1] != ""
}

// hasFlag reports whether --name appears in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// flagValue returns the value of --name (supports --name=value and --name value)
// or def if absent.
func flagValue(args []string, name, def string) string {
	pfx := name + "="
	for i, a := range args {
		if strings.HasPrefix(a, pfx) {
			return strings.TrimPrefix(a, pfx)
		}
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return def
}
