// Package mcpguard statically scans Model Context Protocol servers and tool
// definitions for dangerous capabilities before they are connected to an
// agent runtime.
//
// This file is the FROZEN CONTRACT for the package. The scanner, rules,
// renderers, and tests live in sibling files and must not redefine these
// symbols.
package mcpguard

// Severity classifies the risk of a finding.
type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
	Info     Severity = "info"
)

// Finding is a single detected risk. Rule is a stable identifier such as
// "mcp.shell.unrestricted".
type Finding struct {
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
}

// Report is the result of scanning a target. Result is one of "safe",
// "unsafe", or "review".
type Report struct {
	Target   string    `json:"target"`
	Result   string    `json:"result"`
	Findings []Finding `json:"findings"`
}
