package mcpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeCLI records invocations and returns canned output.
type fakeCLI struct {
	calls    [][]string
	stdout   string
	exitCode int
}

func (f *fakeCLI) run(ctx context.Context, args []string) (string, int, error) {
	f.calls = append(f.calls, args)
	return f.stdout, f.exitCode, nil
}

func serve(t *testing.T, s *Server, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var responses []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("response is not valid JSON: %v (line: %s)", err, sc.Text())
		}
		responses = append(responses, m)
	}
	return responses
}

func TestInitializeHandshake(t *testing.T) {
	s := &Server{Version: "test", Run: (&fakeCLI{}).run}
	resps := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (initialize, ping; the notification is silent), got %d", len(resps))
	}
	result := resps[0]["result"].(map[string]any)
	// Echo the client's requested known version.
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v, want echo of 2025-03-26", result["protocolVersion"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "andbo" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

func TestInitializeUnknownVersionOffersNewest(t *testing.T) {
	s := &Server{Version: "test", Run: (&fakeCLI{}).run}
	resps := serve(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	got := resps[0]["result"].(map[string]any)["protocolVersion"]
	if got != protocolVersions[0] {
		t.Errorf("protocolVersion = %v, want newest %s", got, protocolVersions[0])
	}
}

func TestToolsList(t *testing.T) {
	s := &Server{Version: "test", Run: (&fakeCLI{}).run}
	resps := serve(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := resps[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if m["inputSchema"] == nil {
			t.Errorf("tool %v missing inputSchema", m["name"])
		}
	}
	for _, want := range []string{"sandbox_exec", "sandbox_run", "scan_mcp", "session_list", "session_show"} {
		if !names[want] {
			t.Errorf("missing tool %q in %v", want, names)
		}
	}
}

func TestSandboxExecMapsToCLI(t *testing.T) {
	f := &fakeCLI{stdout: `{"exit_code":0}`}
	s := &Server{Version: "test", Run: f.run}
	resps := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sandbox_exec","arguments":{"command":"go test ./...","dry_run":true}}}`,
	)
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 CLI call, got %d", len(f.calls))
	}
	got := strings.Join(f.calls[0], " ")
	want := "exec --json --dry-run -- sh -lc go test ./..."
	if got != want {
		t.Errorf("cli args = %q, want %q", got, want)
	}
	result := resps[0]["result"].(map[string]any)
	if result["isError"] != false {
		t.Errorf("isError = %v, want false", result["isError"])
	}
}

func TestToolCallNeverEmitsUnsafeFlags(t *testing.T) {
	// Even hostile arguments must not smuggle unsafe flags into the CLI args
	// as separate tokens: the command string stays a single sh -lc argument.
	f := &fakeCLI{stdout: "{}"}
	s := &Server{Version: "test", Run: f.run}
	serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sandbox_exec","arguments":{"command":"x --unsafe --runtime local"}}}`,
	)
	args := f.calls[0]
	for i, a := range args {
		if a == "--unsafe" || a == "--runtime" || a == "--yes-unsafe" || a == "--allow-host-home" {
			t.Errorf("unsafe flag %q leaked into CLI args at %d: %v", a, i, args)
		}
	}
	// The hostile string must be the final sh -lc payload, inert as a flag.
	if args[len(args)-1] != "x --unsafe --runtime local" {
		t.Errorf("command should be a single trailing sh -lc argument, got %v", args)
	}
}

func TestNonZeroExitIsError(t *testing.T) {
	f := &fakeCLI{stdout: `{"exit_code":1}`, exitCode: 1}
	s := &Server{Version: "test", Run: f.run}
	resps := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sandbox_exec","arguments":{"command":"false"}}}`,
	)
	if resps[0]["result"].(map[string]any)["isError"] != true {
		t.Error("non-zero exit should set isError")
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	s := &Server{Version: "test", Run: (&fakeCLI{}).run}
	resps := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm_rf_everything","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	)
	if resps[0]["result"].(map[string]any)["isError"] != true {
		t.Error("unknown tool should be an in-band tool error")
	}
	if resps[1]["error"] == nil {
		t.Error("unknown method should be a JSON-RPC error")
	}
}

func TestMissingRequiredArg(t *testing.T) {
	s := &Server{Version: "test", Run: (&fakeCLI{}).run}
	resps := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sandbox_exec","arguments":{}}}`,
	)
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Error("missing command should be an in-band error")
	}
}
