package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qosikz/andbo/internal/secrets"
)

func newRedactor(t *testing.T, values map[string]string) *secrets.Redactor {
	t.Helper()
	r, err := secrets.NewRedactor(values, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecorderSaveCreatesArtifacts(t *testing.T) {
	base := t.TempDir()
	rec := NewRecorder(base, "fix failing tests", "custom")
	rec.Session.Repository = "."
	rec.Session.Runtime = RuntimeInfo{Engine: "docker", Network: "deny", Image: "img"}
	rec.Event(EvWorkspaceCreated)
	rec.Log("hello world")
	rec.PolicyBlocked("access to .env")

	dir, err := rec.Save(newRedactor(t, nil))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, name := range []string{"session.json", "report.md", "logs.txt", "diff.patch", "policy-events.json", "test-results.txt", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing artifact %s: %v", name, err)
		}
	}
	// No diff -> the "no changes" sentinel.
	data, _ := os.ReadFile(filepath.Join(dir, "diff.patch"))
	if !strings.Contains(string(data), "No code changes detected") {
		t.Errorf("empty diff should record sentinel, got %q", data)
	}
}

func TestSaveRedactsLogs(t *testing.T) {
	base := t.TempDir()
	rec := NewRecorder(base, "task with token supersecretval", "custom")
	rec.Log("printing supersecretval to the log")
	dir, err := rec.Save(newRedactor(t, map[string]string{"MY_SECRET": "supersecretval"}))
	if err != nil {
		t.Fatal(err)
	}
	logs, _ := os.ReadFile(filepath.Join(dir, "logs.txt"))
	if strings.Contains(string(logs), "supersecretval") {
		t.Errorf("secret leaked into logs.txt: %q", logs)
	}
	if !strings.Contains(string(logs), "[REDACTED:MY_SECRET]") {
		t.Errorf("expected redaction placeholder in logs, got %q", logs)
	}
	// session.json (free-text task) must also be redacted.
	sj, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if strings.Contains(string(sj), "supersecretval") {
		t.Errorf("secret leaked into session.json: %q", sj)
	}
}

func TestListAndLoadLatest(t *testing.T) {
	base := t.TempDir()
	red := newRedactor(t, nil)

	r1 := NewRecorder(base, "first", "custom")
	r1.Session.ID = "20260101-000000-aaaaaa"
	if _, err := r1.Save(red); err != nil {
		t.Fatal(err)
	}
	r2 := NewRecorder(base, "second", "custom")
	r2.Session.ID = "20260102-000000-bbbbbb"
	if _, err := r2.Save(red); err != nil {
		t.Fatal(err)
	}

	list, err := List(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0].ID != "20260102-000000-bbbbbb" {
		t.Errorf("newest first expected, got %s", list[0].ID)
	}

	latest, err := Load(base, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Task != "second" {
		t.Errorf("latest task = %q, want second", latest.Task)
	}
}

func TestLoadNoSessions(t *testing.T) {
	if _, err := Load(t.TempDir(), "latest"); err == nil {
		t.Fatal("expected error when no sessions exist")
	}
}

func TestLoadRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	for _, id := range []string{"../secrets", "a/b", `a\b`, "..", "20260101-000000-aaaaaa/../x"} {
		if _, err := Load(base, id); err == nil {
			t.Errorf("Load(%q) should reject path-like ids", id)
		}
	}
}

func TestSessionJSONShape(t *testing.T) {
	base := t.TempDir()
	rec := NewRecorder(base, "t", "custom")
	dir, err := rec.Save(newRedactor(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("session.json not valid JSON: %v", err)
	}
	// Cost should be present with null usd/tokens and status unknown.
	cost, ok := m["cost"].(map[string]any)
	if !ok {
		t.Fatalf("cost missing or wrong type: %v", m["cost"])
	}
	if cost["status"] != "unknown" {
		t.Errorf("cost status = %v, want unknown", cost["status"])
	}
	if cost["usd"] != nil {
		t.Errorf("cost usd should be null, got %v", cost["usd"])
	}
}
