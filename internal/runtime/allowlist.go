package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/qosikz/agentbox/internal/netproxy"
)

// Egress allowlist enforcement.
//
// network.mode=allowlist is enforced with cooperating mechanisms, verified by
// e2e runs against a real engine:
//
//  1. The agent container's ONLY interface is a per-run --internal network,
//     which has no default route — direct IP egress is impossible at the
//     network layer, so anything that ignores the proxy fails closed.
//  2. The agent's DNS is sunk (--dns 0.0.0.0): it cannot resolve any name. The
//     proxy is reached by IP and resolves allowlisted names itself, so the
//     agent never needs DNS. This structurally closes the DNS-tunnel
//     exfiltration channel regardless of the daemon's resolver config.
//  3. A proxy sidecar (the embedded cmd/netproxy binary, run in the SAME
//     runtime image) is dual-homed onto a dedicated per-run EXTERNAL network
//     (not the shared bridge, so unrelated containers can't use it as a relay)
//     and is the only path out, forwarding only HTTP(S) to network.allow hosts.
//
// The sidecar is hardened like the agent container: non-root, all capabilities
// dropped, no privilege escalation, read-only rootfs, PID-capped, --rm, and it
// mounts ONLY the proxy binary (read-only) — never the workspace.

// egressSession tracks the per-run networks, sidecar, and extracted proxy
// binary so they can be torn down when the run ends.
type egressSession struct {
	engine    string
	network   string // per-run INTERNAL network (agent + proxy; no route out)
	extNet    string // per-run EXTERNAL network (proxy only; its route out)
	proxyCtr  string // sidecar container name
	proxyAddr string // host:port of the proxy on the internal network
	tmpDir    string // holds the extracted proxy binary
}

// setupAllowlist provisions the internal network and proxy sidecar for spec.
// On any failure it cleans up whatever was created and returns an actionable
// error: a broken allowlist setup must fail the run, never fall open.
func (r containerRunner) setupAllowlist(ctx context.Context, spec RuntimeSpec) (es *egressSession, err error) {
	sfx := randomSuffix()
	es = &egressSession{
		engine:   r.engine,
		network:  "agentbox-net-" + sfx,
		extNet:   "agentbox-ext-" + sfx,
		proxyCtr: "agentbox-egress-" + sfx,
	}
	defer func() {
		if err != nil {
			es.teardown()
		}
	}()

	// Extract the embedded proxy binary for the engine's architecture.
	arch, archErr := engineArch(ctx, r.engine)
	if archErr != nil {
		return es, archErr
	}
	es.tmpDir, err = os.MkdirTemp("", "agentbox-egress-")
	if err != nil {
		return es, fmt.Errorf("creating temp dir for egress proxy: %w", err)
	}
	binPath, err := netproxy.Extract(arch, es.tmpDir)
	if err != nil {
		return es, err
	}

	// Per-run internal network: the agent's only interface (no route out).
	if out, cerr := r.engineCmd(ctx, "network", "create", "--internal", es.network); cerr != nil {
		return es, fmt.Errorf("creating internal network for egress allowlist: %s", firstLine(out, cerr))
	}
	// Per-run EXTERNAL network for the proxy's egress leg. A dedicated network
	// (rather than the shared default bridge) keeps unrelated containers from
	// reaching the proxy as an open relay.
	if out, cerr := r.engineCmd(ctx, "network", "create", es.extNet); cerr != nil {
		return es, fmt.Errorf("creating external network for egress proxy: %s", firstLine(out, cerr))
	}
	// Proxy sidecar on the internal network...
	args := BuildProxySidecarArgs(spec.Image, es.proxyCtr, es.network, binPath, spec.AllowedDomains)
	if out, cerr := r.engineCmd(ctx, args...); cerr != nil {
		return es, fmt.Errorf("starting egress proxy sidecar: %s", firstLine(out, cerr))
	}
	// ...then dual-homed onto its dedicated external network for real egress.
	if out, cerr := r.engineCmd(ctx, "network", "connect", es.extNet, es.proxyCtr); cerr != nil {
		return es, fmt.Errorf("connecting egress proxy to its external network: %s", firstLine(out, cerr))
	}

	// Wait for the proxy to report READY, then resolve its internal address.
	if err := r.waitProxyReady(ctx, es.proxyCtr); err != nil {
		return es, err
	}
	ip, err := r.containerIPOn(ctx, es.proxyCtr, es.network)
	if err != nil {
		return es, err
	}
	es.proxyAddr = ip + ":3128"
	return es, nil
}

// teardown force-removes the sidecar, both per-run networks, and the extracted
// binary. It uses a fresh context so cleanup still runs after a budget-deadline
// kill, and retries network removal to ride out endpoint-detach latency (a
// network with a still-attached endpoint refuses removal).
func (es *egressSession) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if es.proxyCtr != "" {
		_ = exec.CommandContext(ctx, es.engine, "rm", "-f", es.proxyCtr).Run()
	}
	for _, n := range []string{es.network, es.extNet} {
		if n != "" {
			es.removeNetwork(ctx, n)
		}
	}
	if es.tmpDir != "" {
		_ = os.RemoveAll(es.tmpDir)
	}
}

// removeNetwork removes a per-run network, retrying through the brief window
// where the daemon is still detaching the agent/proxy endpoints. A failure to
// remove leaks a network rather than breaking the run; it is logged to stderr
// (not swallowed) so a leak is at least visible.
func (es *egressSession) removeNetwork(ctx context.Context, name string) {
	for attempt := 0; attempt < 5; attempt++ {
		if out, err := exec.CommandContext(ctx, es.engine, "network", "rm", name).CombinedOutput(); err == nil {
			return
		} else if attempt == 4 {
			fmt.Fprintf(os.Stderr, "agentbox: could not remove egress network %s (leaked): %s\n", name, firstLine(out, err))
			return
		}
		// Best-effort: detach any lingering endpoints, then wait and retry.
		_, _ = es.endpoints(ctx, name)
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// endpoints force-disconnects any containers still attached to name so the
// network can be removed. Best-effort.
func (es *egressSession) endpoints(ctx context.Context, name string) (int, error) {
	out, err := exec.CommandContext(ctx, es.engine, "network", "inspect", name,
		"--format", "{{range $k,$v := .Containers}}{{$k}} {{end}}").Output()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, es.engine, "network", "disconnect", "-f", name, id).Run()
		n++
	}
	return n, nil
}

// collectLog returns the sidecar's audit lines (AGENTBOX-EGRESS ...) so the
// CLI can record them in the session. Best-effort: an unreadable log yields
// nil rather than failing a finished run.
func (es *egressSession) collectLog() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, es.engine, "logs", es.proxyCtr).CombinedOutput()
	if err != nil {
		return nil
	}
	return parseEgressLines(string(out))
}

// waitProxyReady polls the sidecar's logs for the READY line. A proxy that
// never becomes ready fails the run (fail closed).
func (r containerRunner) waitProxyReady(ctx context.Context, ctr string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		out, _ := r.engineCmd(ctx, "logs", ctr)
		if strings.Contains(string(out), netproxy.AuditPrefix+" READY") {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, _ := r.engineCmd(ctx, "logs", ctr)
	return fmt.Errorf("egress proxy did not become ready within 15s; refusing to run without enforcement.\nProxy log: %s", strings.TrimSpace(string(out)))
}

// containerIPOn returns the container's IP address on the named network.
func (r containerRunner) containerIPOn(ctx context.Context, ctr, network string) (string, error) {
	out, err := r.engineCmd(ctx, "inspect", ctr, "--format",
		fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network))
	if err != nil {
		return "", fmt.Errorf("resolving egress proxy address: %s", firstLine(out, err))
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("egress proxy has no address on network %s", network)
	}
	return ip, nil
}

// engineCmd runs the engine CLI and returns combined output.
func (r containerRunner) engineCmd(ctx context.Context, args ...string) ([]byte, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.engine, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// engineArch asks the engine daemon for its (server-side) architecture, since
// containers run with the daemon's arch, not the CLI host's.
func engineArch(ctx context.Context, engine string) (string, error) {
	out, err := exec.CommandContext(ctx, engine, "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		// podman exposes it via `info` on some versions.
		out, err = exec.CommandContext(ctx, engine, "info", "--format", "{{.Host.Arch}}").Output()
		if err != nil {
			return "", fmt.Errorf("detecting %s server architecture for the egress proxy: %v", engine, err)
		}
	}
	arch := normalizeArch(strings.TrimSpace(string(out)))
	if arch == "" {
		return "", fmt.Errorf("unsupported %s server architecture %q for the egress proxy (supported: amd64, arm64)", engine, strings.TrimSpace(string(out)))
	}
	return arch, nil
}

// normalizeArch maps engine-reported architectures onto Go arch names. PURE.
func normalizeArch(s string) string {
	switch strings.ToLower(s) {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
}

// BuildProxySidecarArgs returns the engine CLI arguments that start the egress
// proxy sidecar: detached, on the internal network only (the external network
// is connected afterwards), hardened identically to the agent container, with
// ONLY the proxy binary mounted (read-only) — never the workspace. The
// filesystem is read-only and process/PID count is capped as defense in depth.
// PURE.
func BuildProxySidecarArgs(image, name, network, binPath string, domains []string) []string {
	return []string{
		"run", "-d", "--rm",
		"--name", name,
		"--network", network,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", "10001:10001",
		"--read-only",         // the proxy writes nothing to disk
		"--pids-limit", "256", // bound process/goroutine-thread explosion
		"-v", binPath + ":/agentbox-netproxy:ro",
		"--entrypoint", "/agentbox-netproxy",
		image,
		"-listen", ":3128",
		"-allow", strings.Join(domains, ","),
	}
}

// ApplyAllowlistNetwork rewires agent-container args produced by
// BuildDockerArgs for allowlist mode: the fail-safe "--network none"
// placeholder becomes the per-run internal network, and the standard proxy
// environment variables are injected so proxy-aware tools route through the
// sidecar. If no "--network none" pair is present the args are returned
// unchanged — the container then stays fully isolated (fail closed). PURE.
func ApplyAllowlistNetwork(args []string, network, proxyAddr string) []string {
	idx := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--network" && args[i+1] == "none" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return args
	}
	proxyURL := "http://" + proxyAddr
	env := []string{
		"-e", "HTTP_PROXY=" + proxyURL,
		"-e", "HTTPS_PROXY=" + proxyURL,
		"-e", "http_proxy=" + proxyURL,
		"-e", "https_proxy=" + proxyURL,
		"-e", "NO_PROXY=localhost,127.0.0.1",
		"-e", "no_proxy=localhost,127.0.0.1",
	}
	// Kill DNS resolution inside the agent container: the proxy is reached by
	// IP and resolves allowlisted names itself, so the agent never needs DNS.
	// This structurally closes the DNS-tunnel exfiltration side channel (a
	// container cannot encode data in queries to an attacker nameserver) rather
	// than relying on the --internal network's daemon-dependent DNS behavior.
	dns := []string{"--dns", "0.0.0.0"}
	out := make([]string, 0, len(args)+len(env)+len(dns)+1)
	out = append(out, args[:idx+1]...)
	out = append(out, network)
	out = append(out, dns...)
	out = append(out, env...)
	out = append(out, args[idx+2:]...)
	return out
}

// parseEgressLines extracts the proxy's audit lines from raw logs. PURE.
func parseEgressLines(logs string) []string {
	var lines []string
	for _, line := range strings.Split(logs, "\n") {
		if i := strings.Index(line, netproxy.AuditPrefix); i >= 0 {
			lines = append(lines, strings.TrimSpace(line[i:]))
		}
	}
	return lines
}

// randomSuffix returns 12 hex chars for collision-resistant resource names.
func randomSuffix() string {
	return strings.TrimPrefix(containerName(), "agentbox-")
}

// firstLine condenses engine CLI output (or the exec error) into a single
// actionable line for error messages.
func firstLine(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" && err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
