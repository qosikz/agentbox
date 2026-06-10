package session

import (
	"fmt"
	"strings"
)

// RenderReport renders a session as the Markdown report.md artifact.
func RenderReport(s Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AgentBox Session Report\n\n")
	fmt.Fprintf(&b, "Session: `%s`\n\n", s.ID)

	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- Repository: `%s`\n", orNone(s.Repository))
	fmt.Fprintf(&b, "- Branch: `%s`\n", orNone(s.Branch))
	fmt.Fprintf(&b, "- Agent: `%s`\n", s.Agent)
	fmt.Fprintf(&b, "- Runtime: `%s`\n", orNone(s.Runtime.Engine))
	fmt.Fprintf(&b, "- Network: `%s`\n", orNone(s.Runtime.Network))
	fmt.Fprintf(&b, "- Policy: `%s`\n", orNone(s.Policy.Path))
	fmt.Fprintf(&b, "- Dry run: `%t`\n", s.DryRun)
	fmt.Fprintf(&b, "- Status: `%s`\n\n", s.Result.Status)

	fmt.Fprintf(&b, "## Task\n\n```text\n%s\n```\n\n", s.Task)

	fmt.Fprintf(&b, "## Changed files\n\n")
	if len(s.ChangedFiles) == 0 {
		fmt.Fprintf(&b, "_No code changes detected._\n\n")
	} else {
		for _, f := range s.ChangedFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Policy events\n\n")
	if len(s.PolicyEvents) == 0 {
		fmt.Fprintf(&b, "_No policy events recorded._\n\n")
	} else {
		b.WriteString("```text\n")
		for _, e := range s.PolicyEvents {
			fmt.Fprintf(&b, "%s %s\n", strings.ToUpper(e.Type), e.Detail)
		}
		b.WriteString("```\n\n")
	}

	fmt.Fprintf(&b, "## Tests\n\n")
	if len(s.Tests) == 0 {
		fmt.Fprintf(&b, "_No tests configured._\n\n")
	} else {
		b.WriteString("```text\n")
		for _, t := range s.Tests {
			fmt.Fprintf(&b, "%s: %s\n", t.Command, t.Status)
		}
		b.WriteString("```\n\n")
	}

	fmt.Fprintf(&b, "## Cost\n\n%s\n\n", renderCost(s.Cost))

	fmt.Fprintf(&b, "## Artifacts\n\n")
	for _, name := range []string{"session.json", "report.md", "logs.txt", "diff.patch", "policy-events.json", "test-results.txt", "metadata.json"} {
		fmt.Fprintf(&b, "- `%s`\n", name)
	}

	return b.String()
}

func renderCost(c Cost) string {
	if c.Status != "known" || (c.USD == nil && c.Tokens == nil) {
		return "Unknown. Adapter did not provide cost metadata."
	}
	usd, tokens := "unknown", "unknown"
	if c.USD != nil {
		usd = fmt.Sprintf("$%.2f", *c.USD)
	}
	if c.Tokens != nil {
		tokens = fmt.Sprintf("%d", *c.Tokens)
	}
	return fmt.Sprintf("USD: %s, Tokens: %s", usd, tokens)
}

// RenderShow renders the human-readable `agentbox session show` output.
func RenderShow(s Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s\n\n", s.ID)
	fmt.Fprintf(&b, "Summary\n")
	fmt.Fprintf(&b, "  Status:     %s\n", s.Result.Status)
	fmt.Fprintf(&b, "  Task:       %s\n", s.Task)
	fmt.Fprintf(&b, "  Agent:      %s\n", s.Agent)
	fmt.Fprintf(&b, "  Repository: %s\n", orNone(s.Repository))
	fmt.Fprintf(&b, "  Branch:     %s\n", orNone(s.Branch))
	fmt.Fprintf(&b, "  Runtime:    %s (network %s)\n", orNone(s.Runtime.Engine), orNone(s.Runtime.Network))
	fmt.Fprintf(&b, "  Dry run:    %t\n", s.DryRun)

	fmt.Fprintf(&b, "\nPolicy\n")
	fmt.Fprintf(&b, "  Path: %s\n", orNone(s.Policy.Path))
	fmt.Fprintf(&b, "  Hash: %s\n", orNone(s.Policy.Hash))

	fmt.Fprintf(&b, "\nChanged files\n")
	if len(s.ChangedFiles) == 0 {
		fmt.Fprintf(&b, "  (none)\n")
	} else {
		for _, f := range s.ChangedFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}

	fmt.Fprintf(&b, "\nCommands\n")
	if len(s.Commands) == 0 {
		fmt.Fprintf(&b, "  (none)\n")
	} else {
		for _, c := range s.Commands {
			fmt.Fprintf(&b, "  [exit %d] %s\n", c.ExitCode, c.Cmd)
		}
	}

	fmt.Fprintf(&b, "\nTests\n")
	if len(s.Tests) == 0 {
		fmt.Fprintf(&b, "  (none)\n")
	} else {
		for _, t := range s.Tests {
			fmt.Fprintf(&b, "  %s: %s\n", t.Command, t.Status)
		}
	}

	fmt.Fprintf(&b, "\nPolicy events\n")
	if len(s.PolicyEvents) == 0 {
		fmt.Fprintf(&b, "  (none)\n")
	} else {
		for _, e := range s.PolicyEvents {
			fmt.Fprintf(&b, "  %s %s\n", strings.ToUpper(e.Type), e.Detail)
		}
	}

	fmt.Fprintf(&b, "\nCost\n  %s\n", renderCost(s.Cost))
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
