package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ghNotFoundNote is returned (with Created=false and a nil error) when the
// GitHub CLI is unavailable, so callers can degrade gracefully instead of
// failing the whole run.
const ghNotFoundNote = "gh CLI not found; install GitHub CLI (gh) to open pull requests"

// OpenPR opens a pull request using the GitHub CLI (gh). When gh is not
// installed it returns a PROutput with Created=false and an explanatory Note
// and a nil error, leaving the decision to the caller. When gh is present it
// runs "gh pr create" in the repository directory and parses the printed URL.
func (r *Repo) OpenPR(ctx context.Context, in PRInput) (PROutput, error) {
	// Security/availability gate: never attempt network/auth-bearing gh calls
	// unless the binary actually exists on PATH.
	if _, err := exec.LookPath("gh"); err != nil {
		return PROutput{Created: false, Note: ghNotFoundNote}, nil
	}

	args := []string{"pr", "create",
		"--title", in.Title,
		"--body", in.Body,
		"--base", in.Base,
		"--head", in.Branch,
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return PROutput{Created: false, Note: msg}, fmt.Errorf("gh pr create (base=%s head=%s): %s", in.Base, in.Branch, msg)
	}

	url := parsePRURL(stdout.String())
	return PROutput{URL: url, Created: true}, nil
}

// parsePRURL extracts the first GitHub PR URL printed by "gh pr create". gh
// prints the URL on its own line, but it may emit other lines too, so scan for
// the first http(s) token.
func parsePRURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
				return field
			}
		}
	}
	return strings.TrimSpace(out)
}
