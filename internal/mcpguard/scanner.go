package mcpguard

type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
	Info     Severity = "info"
)

type Finding struct {
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
}

type Report struct {
	Target   string    `json:"target"`
	Result   string    `json:"result"`
	Findings []Finding `json:"findings"`
}

// TODO Phase 7: implement static scanner rules.
func Scan(target string) Report {
	return Report{Target: target, Result: "not_implemented", Findings: nil}
}
