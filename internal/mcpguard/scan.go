package mcpguard

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// skipDirs are directory names never descended into; they hold third-party or
// VCS data that is noise for a capability audit.
var skipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
}

// scannableExts are file extensions whose contents we inspect.
var scannableExts = map[string]bool{
	".py": true,
	".js": true,
	".ts": true,
	".go": true,
}

// scannableNames are exact base names we inspect regardless of extension.
var scannableNames = map[string]bool{
	"package.json":     true,
	"pyproject.toml":   true,
	"requirements.txt": true,
	"go.mod":           true,
	"Dockerfile":       true,
}

// Scan walks target (a file or directory) and returns a Report describing every
// dangerous capability the rule set matched.
//
// Security note: scanning is read-only and never executes any code from the
// target. Result is "unsafe" when any Critical/High finding exists, "review"
// for Medium/Low only, and "safe" when nothing matched.
func Scan(target string) (Report, error) {
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, fmt.Errorf("mcpguard: cannot scan %q: path does not exist; pass a directory or file that exists", target)
		}
		return Report{}, fmt.Errorf("mcpguard: cannot access %q: %w; check the path and permissions", target, err)
	}

	report := Report{Target: target}

	if info.IsDir() {
		err = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !shouldScan(d.Name()) {
				return nil
			}
			rel, relErr := filepath.Rel(target, path)
			if relErr != nil {
				rel = path
			}
			findings, scanErr := scanFile(path, rel)
			if scanErr != nil {
				return scanErr
			}
			report.Findings = append(report.Findings, findings...)
			return nil
		})
		if err != nil {
			return Report{}, fmt.Errorf("mcpguard: failed walking %q: %w; check filesystem permissions", target, err)
		}
	} else {
		if shouldScan(info.Name()) {
			findings, scanErr := scanFile(target, filepath.Base(target))
			if scanErr != nil {
				return Report{}, scanErr
			}
			report.Findings = append(report.Findings, findings...)
		}
	}

	report.Result = classify(report.Findings)
	return report, nil
}

// shouldScan reports whether a file's base name qualifies for inspection. Any
// *.json file is treated as a potential manifest.
func shouldScan(name string) bool {
	if scannableNames[name] {
		return true
	}
	ext := filepath.Ext(name)
	if scannableExts[ext] {
		return true
	}
	if ext == ".json" {
		return true
	}
	return false
}

// scanFile applies every rule to each line of path. rel is the path recorded in
// findings (relative to the scan root).
func scanFile(path, rel string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mcpguard: cannot open %q: %w; check file permissions", path, err)
	}
	defer f.Close()

	var findings []Finding
	scanner := bufio.NewScanner(f)
	// Allow long lines (minified JS, bundled manifests) without erroring.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		for _, r := range rules {
			if r.re.MatchString(text) {
				findings = append(findings, Finding{
					Severity: r.severity,
					Rule:     r.id,
					Message:  r.message,
					File:     rel,
					Line:     line,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("mcpguard: error reading %q: %w; the file may be binary or truncated", path, err)
	}
	return findings, nil
}

// severityRank orders severities for sorting; lower is more severe.
var severityRank = map[Severity]int{
	Critical: 0,
	High:     1,
	Medium:   2,
	Low:      3,
	Info:     4,
}

// classify derives the overall Result from the set of findings.
func classify(findings []Finding) string {
	hasHigh := false
	hasLower := false
	for _, f := range findings {
		switch f.Severity {
		case Critical, High:
			hasHigh = true
		case Medium, Low, Info:
			hasLower = true
		}
	}
	switch {
	case hasHigh:
		return "unsafe"
	case hasLower:
		return "review"
	default:
		return "safe"
	}
}
