package secrets

import (
	"strings"
	"testing"
)

func TestRedactNamedValues(t *testing.T) {
	r, err := NewRedactor(map[string]string{"GITHUB_TOKEN": "supersecretvalue123"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := r.Redact("the token is supersecretvalue123 ok")
	if strings.Contains(out, "supersecretvalue123") {
		t.Errorf("value not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:GITHUB_TOKEN]") {
		t.Errorf("expected named placeholder, got %q", out)
	}
}

func TestRedactBuiltinPatterns(t *testing.T) {
	r, err := NewRedactor(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"key sk-abcdefghijklmnopqrstuvwx live",
		"gh ghp_0123456789abcdefghijABCDEFG token",
		"aws AKIAABCDEFGHIJKLMNOP creds",
	}
	for _, in := range cases {
		out := r.Redact(in)
		if !strings.Contains(out, "[REDACTED:TOKEN]") {
			t.Errorf("pattern not redacted for %q -> %q", in, out)
		}
	}
}

func TestRedactSkipsEmptyAndWildcard(t *testing.T) {
	r, err := NewRedactor(map[string]string{"A": "", "B": "*"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// "*" as a value must not turn every asterisk into a redaction.
	out := r.Redact("a * b")
	if strings.Contains(out, "REDACTED") {
		t.Errorf("wildcard/empty values must not redact, got %q", out)
	}
}

func TestInvalidPatternErrors(t *testing.T) {
	if _, err := NewRedactor(nil, []string{"("}); err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestGatherSecretValues(t *testing.T) {
	env := map[string]string{"GITHUB_TOKEN": "abc", "EMPTY": ""}
	got := GatherSecretValues([]string{"GITHUB_TOKEN", "EMPTY", "MISSING", "*"}, func(k string) string {
		return env[k]
	})
	if got["GITHUB_TOKEN"] != "abc" {
		t.Errorf("expected GITHUB_TOKEN gathered, got %v", got)
	}
	if _, ok := got["EMPTY"]; ok {
		t.Error("empty env value should be skipped")
	}
	if _, ok := got["MISSING"]; ok {
		t.Error("missing env value should be skipped")
	}
	if _, ok := got["*"]; ok {
		t.Error("wildcard should be skipped")
	}
}

func TestRedactLongerValuesFirst(t *testing.T) {
	// Overlapping values: the longer one should win cleanly.
	r, err := NewRedactor(map[string]string{"SHORT": "secret", "LONG": "secretlonger"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := r.Redact("here is secretlonger value")
	if strings.Contains(out, "secretlonger") {
		t.Errorf("longer value not fully redacted: %q", out)
	}
}
