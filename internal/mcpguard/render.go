package mcpguard

import (
	"encoding/json"
	"sort"
	"strings"
)

// footer is the mandatory disclaimer appended to every human-readable report.
// It states the analysis is static and that findings must be reviewed, which is
// why pragmatic, false-positive-prone rules are acceptable.
const footer = "Static analysis only. Review findings before trusting the server."

// resultPhrase maps a Report.Result to a human sentence for the summary line.
func resultPhrase(result string) string {
	switch result {
	case "unsafe":
		return "unsafe - critical or high-risk capabilities detected"
	case "review":
		return "review - moderate or low-risk capabilities detected"
	case "safe":
		return "safe - no risky capabilities detected"
	default:
		return result
	}
}

// Human renders the report as plain text: a header, the findings sorted by
// severity (critical first), a result summary, and the mandatory footer.
func (r Report) Human() string {
	var b strings.Builder

	b.WriteString("MCP Guard scan: ")
	b.WriteString(r.Target)
	b.WriteString("\n\n")

	// Sort a copy so rendering never mutates the caller's slice. Ties break by
	// file then line for stable, deterministic output.
	sorted := make([]Finding, len(r.Findings))
	copy(sorted, r.Findings)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := severityRank[sorted[i].Severity], severityRank[sorted[j].Severity]
		if ri != rj {
			return ri < rj
		}
		if sorted[i].File != sorted[j].File {
			return sorted[i].File < sorted[j].File
		}
		return sorted[i].Line < sorted[j].Line
	})

	if len(sorted) == 0 {
		b.WriteString("No findings.\n")
	} else {
		for _, f := range sorted {
			b.WriteString(strings.ToUpper(string(f.Severity)))
			b.WriteString("  ")
			b.WriteString(f.Message)
			b.WriteString(" (")
			b.WriteString(f.File)
			b.WriteString(":")
			b.WriteString(itoa(f.Line))
			b.WriteString(")\n")
		}
	}

	b.WriteString("\n")
	b.WriteString("Result: ")
	b.WriteString(resultPhrase(r.Result))
	b.WriteString("\n")
	b.WriteString(footer)
	b.WriteString("\n")

	return b.String()
}

// itoa is a tiny non-allocating-ish int formatter kept local to avoid pulling
// in strconv solely for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// JSON returns the report as indented JSON.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
