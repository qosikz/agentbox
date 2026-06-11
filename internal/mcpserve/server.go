// Package mcpserve implements a minimal Model Context Protocol (MCP) stdio
// server that exposes AgentBox's sandbox operations as tools, so any
// MCP-capable agent harness (Claude Code, OpenClaw, Codex CLI, Gemini CLI,
// Goose, OpenCode, ...) can run commands and agents in isolated, policy-
// controlled workspaces.
//
// Transport: newline-delimited JSON-RPC 2.0 over stdio. Nothing but protocol
// messages may be written to stdout; diagnostics go to stderr.
//
// Security: tool calls execute the agentbox CLI itself (self-exec) with
// --json, under the policy file in the working directory. Unsafe modes
// (--unsafe, --runtime local, --allow-*) are NOT reachable through MCP tools:
// a harness must never be able to silently escalate past the policy.
package mcpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// protocolVersions lists MCP protocol revisions this server accepts, newest
// first. If the client requests a known version it is echoed back; otherwise
// the newest supported version is offered (per spec negotiation). 2025-11-25
// is the current stable revision as of June 2026; older revisions are kept
// because several clients still send them.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// CLIRunner executes the agentbox CLI and returns its stdout, exit code, and
// any spawn error. Injected so tests can stub the CLI.
type CLIRunner func(ctx context.Context, args []string) (stdout string, exitCode int, err error)

// Server is a minimal tools-only MCP server.
type Server struct {
	Version string    // agentbox version, reported in serverInfo
	Run     CLIRunner // tool executor; defaults to self-exec
}

// New returns a Server that self-execs the current agentbox binary.
func New(version string) *Server {
	return &Server{Version: version, Run: selfExec}
}

// selfExec runs the current executable with args, capturing stdout. The CLI's
// stderr is forwarded to our stderr (never stdout, which carries the protocol).
func selfExec(ctx context.Context, args []string) (string, int, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", -1, fmt.Errorf("locating agentbox binary: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return out.String(), exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return out.String(), -1, runErr
	}
	return out.String(), 0, nil
}

// --- JSON-RPC plumbing ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Serve processes newline-delimited JSON-RPC messages from in until EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate large tool args

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParse, Message: "parse error: " + err.Error()}})
			continue
		}
		resp, respond := s.dispatch(ctx, req)
		if respond {
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("writing MCP response: %w", err)
			}
		}
	}
	return sc.Err()
}

// dispatch routes one message. Notifications (no id) get no response.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	isNotification := len(req.ID) == 0
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = s.initializeResult(req.Params)
	case "notifications/initialized", "initialized":
		return resp, false
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		resp.Result = s.callTool(ctx, req.Params)
	default:
		if isNotification {
			return resp, false // unknown notifications are ignored per spec
		}
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
	return resp, !isNotification
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	requested := ""
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil {
		requested = p.ProtocolVersion
	}
	version := protocolVersions[0]
	for _, v := range protocolVersions {
		if v == requested {
			version = v
			break
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "agentbox", "version": s.Version},
		"instructions": "AgentBox provides isolated, policy-controlled sandboxes. " +
			"Use sandbox_exec to safely try commands or validate generated code, " +
			"sandbox_run to drive a coding agent on a task, scan_mcp to vet an MCP " +
			"server before trusting it, and the session tools to audit past runs. " +
			"All runs are governed by the agentbox.yaml policy in the working " +
			"directory; unsafe modes are not available through this server.",
	}
}

// --- tools ---

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefinitions() []toolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	return []toolDef{
		{
			Name: "sandbox_exec",
			Description: "Run a shell command inside an isolated AgentBox workspace " +
				"(container by default, network denied, sensitive files excluded, " +
				"session recorded). Use this to safely test commands, run test " +
				"suites against generated code, or try anything risky. Returns " +
				"exit code, output, and changed files. Set dry_run to preview " +
				"the sandbox configuration without executing.",
			InputSchema: obj(map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command to run in the sandbox"},
				"dry_run": map[string]any{"type": "boolean", "description": "Preview only; do not execute"},
			}, "command"),
		},
		{
			Name: "sandbox_run",
			Description: "Run a coding-agent task inside an isolated AgentBox " +
				"workspace using the agent adapter configured in agentbox.yaml " +
				"(or the agent argument). The agent edits a disposable copy; " +
				"diffs, tests, and logs are captured in a session.",
			InputSchema: obj(map[string]any{
				"task":    map[string]any{"type": "string", "description": "Task for the agent"},
				"agent":   map[string]any{"type": "string", "description": "Adapter name (e.g. custom, claude)"},
				"dry_run": map[string]any{"type": "boolean", "description": "Preview only; do not execute"},
			}, "task"),
		},
		{
			Name: "scan_mcp",
			Description: "Statically scan an MCP server directory for dangerous " +
				"capabilities (shell execution, secret access, broad filesystem/" +
				"network reach) BEFORE trusting or connecting it. Returns findings " +
				"with severities. Static analysis only — review findings.",
			InputSchema: obj(map[string]any{
				"path": map[string]any{"type": "string", "description": "Local path to the MCP server source"},
			}, "path"),
		},
		{
			Name:        "session_list",
			Description: "List recorded AgentBox sessions (audit log of every sandboxed run).",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "session_show",
			Description: "Show one recorded session: result, changed files, commands, tests, policy events.",
			InputSchema: obj(map[string]any{
				"id": map[string]any{"type": "string", "description": "Session id, or 'latest' (default)"},
			}),
		},
	}
}

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// callTool executes a tool and always returns a tools/call result object
// (errors are in-band via isError, per MCP).
func (s *Server) callTool(ctx context.Context, params json.RawMessage) map[string]any {
	var p callParams
	if err := json.Unmarshal(params, &p); err != nil {
		return toolError("invalid tools/call params: " + err.Error())
	}
	args, err := cliArgsFor(p)
	if err != nil {
		return toolError(err.Error())
	}

	stdout, exitCode, runErr := s.Run(ctx, args)
	if runErr != nil {
		return toolError("agentbox execution failed: " + runErr.Error())
	}

	text := strings.TrimSpace(stdout)
	if text == "" {
		text = fmt.Sprintf("(no output; exit code %d)", exitCode)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": exitCode != 0,
	}
}

// cliArgsFor maps a tool call onto agentbox CLI arguments. Only safe,
// policy-governed operations are exposed; no unsafe flag is ever emitted.
func cliArgsFor(p callParams) ([]string, error) {
	str := func(key string) string {
		v, _ := p.Arguments[key].(string)
		return strings.TrimSpace(v)
	}
	boolean := func(key string) bool {
		v, _ := p.Arguments[key].(bool)
		return v
	}

	switch p.Name {
	case "sandbox_exec":
		cmd := str("command")
		if cmd == "" {
			return nil, fmt.Errorf("sandbox_exec requires a non-empty 'command'")
		}
		args := []string{"exec", "--json"}
		if boolean("dry_run") {
			args = append(args, "--dry-run")
		}
		return append(args, "--", "sh", "-lc", cmd), nil

	case "sandbox_run":
		task := str("task")
		if task == "" {
			return nil, fmt.Errorf("sandbox_run requires a non-empty 'task'")
		}
		args := []string{"run", task, "--json"}
		if agent := str("agent"); agent != "" {
			args = append(args, "--agent", agent)
		}
		if boolean("dry_run") {
			args = append(args, "--dry-run")
		}
		return args, nil

	case "scan_mcp":
		path := str("path")
		if path == "" {
			return nil, fmt.Errorf("scan_mcp requires a non-empty 'path'")
		}
		return []string{"mcp", "scan", path, "--json"}, nil

	case "session_list":
		return []string{"session", "list", "--json"}, nil

	case "session_show":
		id := str("id")
		if id == "" {
			id = "latest"
		}
		return []string{"session", "show", id, "--json"}, nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}
