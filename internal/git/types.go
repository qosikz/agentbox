// Package git wraps the system git (and optionally gh) CLIs to detect repos,
// generate diffs, branch, commit, and open pull requests.
//
// This file is the FROZEN CONTRACT for the package. Implementations live in
// sibling files and must not redefine these symbols.
package git

// Repo is a handle to a git working tree rooted at Dir.
type Repo struct {
	Dir string
}

// PRInput describes a pull request to open.
type PRInput struct {
	Title  string
	Body   string
	Branch string
	Base   string
}

// PROutput is the result of opening (or attempting to open) a PR.
type PROutput struct {
	URL     string
	Created bool
	// Note explains why a PR was not created (e.g. gh CLI unavailable), if
	// Created is false.
	Note string
}
