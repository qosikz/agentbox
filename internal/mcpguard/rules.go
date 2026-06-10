package mcpguard

import "regexp"

// rule is a single line-based detector. A match anywhere on a line produces a
// Finding with the rule's Severity, ID, and Message.
//
// Security note: these rules are intentionally pragmatic and err toward false
// positives. Static analysis cannot prove a capability is safe; the goal is to
// surface every place a server *could* reach the shell, secrets, the
// filesystem, the network, or a database so a human can review it. The Human()
// footer states this limitation explicitly.
type rule struct {
	severity Severity
	id       string
	message  string
	re       *regexp.Regexp
}

// rules is the ordered, stable rule set applied to every scanned line. Patterns
// are case-sensitive except where a rule deliberately uses (?i).
var rules = []rule{
	// mcp.shell.unrestricted (Critical): any path to executing a shell command.
	{
		severity: Critical,
		id:       "mcp.shell.unrestricted",
		message:  "unrestricted shell or process execution",
		re: regexp.MustCompile(
			`child_process|\.execSync\s*\(|\.exec\s*\(|\bspawn\s*\(|\bsubprocess\.|os\.system\s*\(|exec\.Command\s*\(|shell\s*=\s*True`,
		),
	},

	// mcp.env.secrets (High): reading process/OS environment, or referencing an
	// identifier that looks like a credential.
	{
		severity: High,
		id:       "mcp.env.secrets",
		message:  "environment or secret access",
		re: regexp.MustCompile(
			`process\.env|os\.environ|[A-Za-z0-9_]*(TOKEN|SECRET|KEY|PASSWORD|CREDENTIAL)[A-Za-z0-9_]*`,
		),
	},

	// mcp.fs.unrestricted (High): touching sensitive paths or doing broad,
	// data-dependent file reads/writes.
	{
		severity: High,
		id:       "mcp.fs.unrestricted",
		message:  "unrestricted filesystem access",
		re: regexp.MustCompile(
			`~/|/etc/|\.env\b|\.ssh\b|\.aws\b|fs\.(read|write)File(Sync)?\s*\(|\bopen\s*\(`,
		),
	},

	// mcp.net.unrestricted (Medium): outbound HTTP clients.
	{
		severity: Medium,
		id:       "mcp.net.unrestricted",
		message:  "unrestricted network request",
		re: regexp.MustCompile(
			`requests\.(get|post|put|delete|patch)\s*\(|\bfetch\s*\(|\baxios\b|http\.(get|post)\s*\(|\burllib\b|net/http|http\.Client`,
		),
	},

	// mcp.db.write (Medium): SQL mutation keywords.
	{
		severity: Medium,
		id:       "mcp.db.write",
		message:  "database write or schema change",
		re:       regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|DROP)\b`),
	},

	// mcp.prompt_injection.surface (Low): tool/description text that smells like
	// a prompt-injection payload.
	{
		severity: Low,
		id:       "mcp.prompt_injection.surface",
		message:  "possible prompt-injection surface in tool text",
		re:       regexp.MustCompile(`(?i)ignore previous|system prompt|disregard`),
	},
}
