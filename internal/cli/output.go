package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Exit codes per the CLI UX specification (§8).
const (
	ExitOK              = 0
	ExitGeneral         = 1
	ExitPolicyViolation = 2
	ExitRuntimeUnavail  = 3
	ExitAgentFailed     = 4
	ExitTestsFailed     = 5
	ExitGitFailed       = 6
	ExitInvalidConfig   = 7
	ExitUnsafeRequired  = 8
)

// CodedError carries an exit code alongside an error so main can map it.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

// coded wraps err with an exit code.
func coded(code int, err error) error {
	if err == nil {
		return nil
	}
	return &CodedError{Code: code, Err: err}
}

// codedf wraps a formatted message with an exit code.
func codedf(code int, format string, args ...any) error {
	return &CodedError{Code: code, Err: fmt.Errorf(format, args...)}
}

// CodeFor returns the exit code for err (1 for plain errors, 0 for nil).
func CodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	if ce, ok := err.(*CodedError); ok {
		return ce.Code
	}
	return ExitGeneral
}

// ok prints a green check line.
func ok(w io.Writer, msg string) {
	fmt.Fprintf(w, "✓ %s\n", msg)
}

// warn prints a warning line to stderr.
func warn(msg string) { warnTo(os.Stderr, msg) }

// warnTo prints a warning line to w. Commands that thread a writer use this so
// their diagnostics land on the caller's stream rather than the process's.
func warnTo(w io.Writer, msg string) {
	fmt.Fprintf(w, "! %s\n", msg)
}

// printJSON marshals v as indented JSON to w.
func printJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
