// Package session records an auditable artifact for every AgentBox run.
//
// A session lives under .agentbox/sessions/<id>/ and contains the structured
// record (session.json), a human report (report.md), redacted logs, the diff,
// policy events, and test results. All persisted text passes through secret
// redaction before it touches disk.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/qosikz/agentbox/internal/secrets"
)

// Session is the structured record persisted as session.json.
type Session struct {
	ID           string          `json:"id"`
	StartedAt    time.Time       `json:"started_at"`
	EndedAt      time.Time       `json:"ended_at"`
	Repository   string          `json:"repository"`
	Branch       string          `json:"branch"`
	Agent        string          `json:"agent"`
	Task         string          `json:"task"`
	DryRun       bool            `json:"dry_run"`
	Runtime      RuntimeInfo     `json:"runtime"`
	Policy       PolicyRef       `json:"policy"`
	ChangedFiles []string        `json:"changed_files"`
	Commands     []ExecutedCmd   `json:"commands"`
	PolicyEvents []PolicyEvent   `json:"policy_events"`
	Tests        []TestRun       `json:"tests"`
	Cost         Cost            `json:"cost"`
	Result       Result          `json:"result"`
	Timeline     []TimelineEvent `json:"timeline"`
}

// RuntimeInfo records how the run was executed.
type RuntimeInfo struct {
	Engine  string `json:"engine"`
	Network string `json:"network"`
	Image   string `json:"image"`
}

// PolicyRef identifies the policy file and its content hash.
type PolicyRef struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// ExecutedCmd is a command the agent or runtime executed.
type ExecutedCmd struct {
	Cmd      string `json:"cmd"`
	ExitCode int    `json:"exit_code"`
}

// PolicyEvent records an enforcement decision (e.g. a blocked path).
type PolicyEvent struct {
	Type   string `json:"type"`   // e.g. "blocked", "info"
	Detail string `json:"detail"` // e.g. "access to .env"
}

// TestRun records a single test command and its outcome.
type TestRun struct {
	Command string `json:"command"`
	Status  string `json:"status"` // passed | failed | skipped
	Output  string `json:"output,omitempty"`
}

// Cost holds optional spend/usage data. Status is "unknown" when the adapter
// does not provide metadata; values are then nil (JSON null), never faked.
type Cost struct {
	USD    *float64 `json:"usd"`
	Tokens *int64   `json:"tokens"`
	Status string   `json:"status"`
}

// Result is the overall outcome.
type Result struct {
	Status  string `json:"status"` // success | failed | blocked
	Summary string `json:"summary"`
}

// TimelineEvent is a single ordered event in the run.
type TimelineEvent struct {
	Event string    `json:"event"`
	Time  time.Time `json:"time"`
}

// Timeline event name constants (see session spec §8).
const (
	EvWorkspaceCreated = "workspace.created"
	EvPolicyLoaded     = "policy.loaded"
	EvRuntimeStarted   = "runtime.started"
	EvAgentStarted     = "agent.started"
	EvCommandExecuted  = "command.executed"
	EvPolicyBlocked    = "policy.blocked"
	EvFileChanged      = "file.changed"
	EvTestsStarted     = "tests.started"
	EvTestsCompleted   = "tests.completed"
	EvGitDiffGenerated = "git.diff.generated"
	EvSessionCompleted = "session.completed"
)

// Recorder accumulates a session in memory and writes it to disk on Save.
type Recorder struct {
	Session  *Session
	Diff     []byte
	Metadata map[string]string

	baseDir string
	logs    strings.Builder
}

// NewRecorder creates a recorder with a fresh session id and start time.
func NewRecorder(baseDir, task, agent string) *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		baseDir: baseDir,
		Session: &Session{
			ID:           now.Format("20060102-150405") + "-" + shortID(),
			StartedAt:    now,
			Task:         task,
			Agent:        agent,
			ChangedFiles: []string{},
			Commands:     []ExecutedCmd{},
			PolicyEvents: []PolicyEvent{},
			Tests:        []TestRun{},
			Timeline:     []TimelineEvent{},
			Cost:         Cost{Status: "unknown"},
			Result:       Result{Status: "success", Summary: ""},
		},
		Metadata: map[string]string{},
	}
}

// Event appends a timeline event stamped at the current time.
func (r *Recorder) Event(name string) {
	r.Session.Timeline = append(r.Session.Timeline, TimelineEvent{Event: name, Time: time.Now().UTC()})
}

// Log appends a line to the raw log buffer.
func (r *Recorder) Log(line string) {
	r.logs.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		r.logs.WriteByte('\n')
	}
}

// Logf appends a formatted log line.
func (r *Recorder) Logf(format string, args ...any) {
	r.Log(fmt.Sprintf(format, args...))
}

// PolicyBlocked records a blocked policy event and a matching timeline entry.
func (r *Recorder) PolicyBlocked(detail string) {
	r.Session.PolicyEvents = append(r.Session.PolicyEvents, PolicyEvent{Type: "blocked", Detail: detail})
	r.Event(EvPolicyBlocked)
}

// Dir returns the directory this session will be (or was) written to.
func (r *Recorder) Dir() string {
	return filepath.Join(SessionsDir(r.baseDir), r.Session.ID)
}

// Save writes all session artifacts, redacting every text file through red.
func (r *Recorder) Save(red *secrets.Redactor) (string, error) {
	if r.Session.EndedAt.IsZero() {
		r.Session.EndedAt = time.Now().UTC()
	}
	r.Event(EvSessionCompleted)

	dir := r.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating session dir %s: %w", dir, err)
	}

	redacted := redactSession(*r.Session, red)

	data, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling session: %w", err)
	}
	if err := writeFile(dir, "session.json", append(data, '\n')); err != nil {
		return "", err
	}

	if err := writeFile(dir, "report.md", []byte(red.Redact(RenderReport(redacted)))); err != nil {
		return "", err
	}

	if err := writeFile(dir, "logs.txt", []byte(red.Redact(r.logs.String()))); err != nil {
		return "", err
	}

	peData, err := json.MarshalIndent(redacted.PolicyEvents, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling policy events: %w", err)
	}
	if err := writeFile(dir, "policy-events.json", append(peData, '\n')); err != nil {
		return "", err
	}

	diff := r.Diff
	if len(diff) == 0 {
		diff = []byte("No code changes detected.\n")
	}
	if err := writeFile(dir, "diff.patch", []byte(red.Redact(string(diff)))); err != nil {
		return "", err
	}

	if err := writeFile(dir, "test-results.txt", []byte(red.Redact(renderTestResults(redacted.Tests)))); err != nil {
		return "", err
	}

	meta := map[string]string{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"engine":  r.Session.Runtime.Engine,
		"dry_run": fmt.Sprintf("%t", r.Session.DryRun),
	}
	for k, v := range r.Metadata {
		meta[k] = v
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling metadata: %w", err)
	}
	if err := writeFile(dir, "metadata.json", append(metaData, '\n')); err != nil {
		return "", err
	}

	return dir, nil
}

func writeFile(dir, name string, data []byte) error {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

func renderTestResults(tests []TestRun) string {
	if len(tests) == 0 {
		return "No tests configured.\n"
	}
	var b strings.Builder
	for _, t := range tests {
		fmt.Fprintf(&b, "$ %s\n%s\n%s\n\n", t.Command, t.Status, t.Output)
	}
	return b.String()
}

// redactSession returns a copy of s with free-text fields redacted.
func redactSession(s Session, red *secrets.Redactor) Session {
	s.Task = red.Redact(s.Task)
	s.Result.Summary = red.Redact(s.Result.Summary)

	cmds := make([]ExecutedCmd, len(s.Commands))
	for i, c := range s.Commands {
		c.Cmd = red.Redact(c.Cmd)
		cmds[i] = c
	}
	s.Commands = cmds

	evs := make([]PolicyEvent, len(s.PolicyEvents))
	for i, e := range s.PolicyEvents {
		e.Detail = red.Redact(e.Detail)
		evs[i] = e
	}
	s.PolicyEvents = evs

	tests := make([]TestRun, len(s.Tests))
	for i, t := range s.Tests {
		t.Output = red.Redact(t.Output)
		tests[i] = t
	}
	s.Tests = tests

	files := make([]string, len(s.ChangedFiles))
	for i, f := range s.ChangedFiles {
		files[i] = red.Redact(f)
	}
	s.ChangedFiles = files

	return s
}

func shortID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a fixed marker; ids only need to disambiguate within a
		// timestamp second, and Save would surface any real write failure.
		return "000000"
	}
	return hex.EncodeToString(b)
}

// SessionsDir returns the sessions root for a workspace base directory.
func SessionsDir(base string) string {
	return filepath.Join(base, ".agentbox", "sessions")
}

// List returns all sessions under base, newest first.
func List(base string) ([]Session, error) {
	root := SessionsDir(base)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading sessions: %w", err)
	}
	var out []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := readSession(filepath.Join(root, e.Name()))
		if err != nil {
			continue // skip unreadable/partial sessions
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Load returns a single session by id; "latest" (or "") resolves to the newest.
func Load(base, id string) (Session, error) {
	if id == "" || id == "latest" {
		list, err := List(base)
		if err != nil {
			return Session{}, err
		}
		if len(list) == 0 {
			return Session{}, fmt.Errorf("no sessions found under %s", SessionsDir(base))
		}
		return list[0], nil
	}
	// Security: a session id is a single directory name. Reject path
	// separators and parent references so a crafted id cannot escape the
	// sessions directory.
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return Session{}, fmt.Errorf("invalid session id %q; run 'agentbox session list' to see valid ids", id)
	}
	return readSession(filepath.Join(SessionsDir(base), id))
}

func readSession(dir string) (Session, error) {
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("parsing %s: %w", dir, err)
	}
	return s, nil
}
