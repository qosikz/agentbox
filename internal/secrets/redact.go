// Package secrets redacts sensitive values from text that AgentBox persists
// (logs, reports, session metadata). Redaction is a defense-in-depth control:
// it cannot guarantee every secret format is caught, so the security model
// treats it as best-effort and documents that limitation.
package secrets

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// BuiltinPatterns matches common credential formats by shape. They are applied
// in addition to any user-supplied redact_patterns and known secret values.
var BuiltinPatterns = []string{
	`sk-ant-[A-Za-z0-9_-]{8,}`,            // Anthropic
	`sk-[A-Za-z0-9_-]{16,}`,               // OpenAI and similar
	`ghp_[A-Za-z0-9]{20,}`,                // GitHub personal access token
	`gho_[A-Za-z0-9]{20,}`,                // GitHub OAuth token
	`ghs_[A-Za-z0-9]{20,}`,                // GitHub server token
	`github_pat_[A-Za-z0-9_]{20,}`,        // GitHub fine-grained PAT
	`AKIA[0-9A-Z]{16}`,                    // AWS access key id
	`AIza[0-9A-Za-z_\-]{20,}`,             // Google API key
	`xox[baprs]-[A-Za-z0-9-]{10,}`,        // Slack token
	`-----BEGIN [A-Z ]*PRIVATE KEY-----`,  // PEM private key block
}

// Redactor replaces known secret values and pattern matches with placeholders.
// The zero value is not usable; construct with NewRedactor.
type Redactor struct {
	// names holds (value -> name) so values can be replaced with a named tag.
	named    []namedValue
	patterns []*regexp.Regexp
}

type namedValue struct {
	value string
	name  string
}

// NewRedactor builds a Redactor from known name->value secrets and extra regex
// patterns. Built-in patterns are always included. An invalid extra pattern is
// returned as an error so callers can warn the user.
func NewRedactor(values map[string]string, extraPatterns []string) (*Redactor, error) {
	r := &Redactor{}
	for name, val := range values {
		if val == "" || val == "*" {
			continue
		}
		r.named = append(r.named, namedValue{value: val, name: name})
	}
	// Replace longer values first to avoid leaving fragments behind.
	sort.SliceStable(r.named, func(i, j int) bool {
		return len(r.named[i].value) > len(r.named[j].value)
	})

	all := append([]string{}, BuiltinPatterns...)
	all = append(all, extraPatterns...)
	for _, p := range all {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid redact pattern %q: %w", p, err)
		}
		r.patterns = append(r.patterns, re)
	}
	return r, nil
}

// Redact returns s with known secret values and pattern matches replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	for _, nv := range r.named {
		s = strings.ReplaceAll(s, nv.value, "[REDACTED:"+nv.name+"]")
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, "[REDACTED:TOKEN]")
	}
	return s
}

// GatherSecretValues reads the named secrets from the environment so their
// values can be redacted from logs even when (especially when) the policy
// denies passing them to the agent. env is typically os.Getenv. Names equal to
// "*" are skipped because a wildcard has no single value to redact.
func GatherSecretValues(names []string, env func(string) string) map[string]string {
	out := map[string]string{}
	for _, name := range names {
		if name == "" || name == "*" {
			continue
		}
		if v := env(name); v != "" {
			out[name] = v
		}
	}
	return out
}
