package mcpguard

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func hasSeverity(findings []Finding, sev Severity) bool {
	for _, f := range findings {
		if f.Severity == sev {
			return true
		}
	}
	return false
}

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func TestScanResult(t *testing.T) {
	tests := []struct {
		name           string
		target         string
		wantResult     string
		wantCritical   bool
		forbidCritical bool
		wantRule       string
	}{
		{
			name:         "unsafe server flags shell execution",
			target:       filepath.Join("testdata", "unsafe"),
			wantResult:   "unsafe",
			wantCritical: true,
			wantRule:     "mcp.shell.unrestricted",
		},
		{
			name:           "safe server has no critical findings",
			target:         filepath.Join("testdata", "safe"),
			forbidCritical: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := Scan(tt.target)
			if err != nil {
				t.Fatalf("Scan(%q) returned error: %v", tt.target, err)
			}

			if tt.wantResult != "" && report.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q (findings: %+v)", report.Result, tt.wantResult, report.Findings)
			}

			if tt.wantCritical && !hasSeverity(report.Findings, Critical) {
				t.Errorf("expected at least one Critical finding, got %+v", report.Findings)
			}

			if tt.forbidCritical {
				if hasSeverity(report.Findings, Critical) {
					t.Errorf("expected no Critical findings, got %+v", report.Findings)
				}
				if report.Result != "safe" && report.Result != "review" {
					t.Errorf("Result = %q, want \"safe\" or \"review\"", report.Result)
				}
			}

			if tt.wantRule != "" && !hasRule(report.Findings, tt.wantRule) {
				t.Errorf("expected rule %q in findings, got %+v", tt.wantRule, report.Findings)
			}
		})
	}
}

func TestScanFindingMetadata(t *testing.T) {
	report, err := Scan(filepath.Join("testdata", "unsafe"))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected findings for unsafe fixture, got none")
	}
	for _, f := range report.Findings {
		if f.File == "" {
			t.Errorf("finding %+v has empty File", f)
		}
		if filepath.IsAbs(f.File) {
			t.Errorf("finding File %q should be relative to scan root", f.File)
		}
		if f.Line < 1 {
			t.Errorf("finding %+v has non-positive (non 1-based) Line", f)
		}
	}
}

func TestScanNonExistentPath(t *testing.T) {
	_, err := Scan(filepath.Join("testdata", "does-not-exist-xyz"))
	if err == nil {
		t.Fatal("expected an error for a non-existent path, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist-xyz") {
		t.Errorf("error should name the offending path, got: %v", err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	report, err := Scan(filepath.Join("testdata", "unsafe"))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() returned error: %v", err)
	}

	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("JSON() output did not unmarshal into Report: %v", err)
	}

	if got.Target != report.Target {
		t.Errorf("round-trip Target = %q, want %q", got.Target, report.Target)
	}
	if got.Result != report.Result {
		t.Errorf("round-trip Result = %q, want %q", got.Result, report.Result)
	}
	if len(got.Findings) != len(report.Findings) {
		t.Errorf("round-trip Findings len = %d, want %d", len(got.Findings), len(report.Findings))
	}
}

func TestHumanFooterAndOrder(t *testing.T) {
	report, err := Scan(filepath.Join("testdata", "unsafe"))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	out := report.Human()

	if !strings.Contains(out, footer) {
		t.Errorf("Human() output missing mandatory footer %q; got:\n%s", footer, out)
	}
	if !strings.HasPrefix(out, "MCP Guard scan: ") {
		t.Errorf("Human() should start with header, got:\n%s", out)
	}
	if !strings.Contains(out, "Result: ") {
		t.Errorf("Human() output missing result line; got:\n%s", out)
	}

	// Critical findings must render before lower-severity ones.
	criticalIdx := strings.Index(out, strings.ToUpper(string(Critical)))
	mediumIdx := strings.Index(out, strings.ToUpper(string(Medium)))
	if criticalIdx == -1 {
		t.Fatalf("expected a CRITICAL line in Human() output:\n%s", out)
	}
	if mediumIdx != -1 && criticalIdx > mediumIdx {
		t.Errorf("CRITICAL should sort before MEDIUM; got:\n%s", out)
	}
}

func TestHumanSafeFixture(t *testing.T) {
	report, err := Scan(filepath.Join("testdata", "safe"))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	out := report.Human()
	if !strings.Contains(out, footer) {
		t.Errorf("Human() output missing mandatory footer for safe fixture:\n%s", out)
	}
}
