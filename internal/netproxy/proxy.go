// Package netproxy implements AgentBox's egress-enforcement proxy: a small
// filtering forward proxy that is the ONLY path out of the sandbox network.
//
// Enforcement model: the agent container is
// attached to a per-run --internal container network, which has no default
// route — direct egress is impossible at the network layer, so anything that
// ignores the proxy fails closed. This proxy runs in a sidecar container that
// is also attached to an external network, and forwards only requests whose
// target host matches the policy's network.allow domain list.
//
// Supported traffic: HTTPS via HTTP CONNECT, and plain HTTP via absolute-form
// proxy requests — the forms every proxy-aware client (curl, git, npm, pip,
// model-API SDKs) uses with HTTP(S)_PROXY. Everything else cannot leave the
// internal network at all.
//
// The package is stdlib-only and OS-independent; only the cmd/netproxy binary
// is cross-compiled for linux containers.
package netproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// AuditPrefix starts every audit line the proxy writes to its log. The runner
// scans container logs for this prefix to turn denials into session policy
// events, so it must stay stable.
const AuditPrefix = "AGENTBOX-EGRESS"

const (
	// tunnelIdle bounds an idle CONNECT tunnel so a half-open/slowloris client
	// cannot pin sockets and goroutines forever.
	tunnelIdle = 2 * time.Minute
	// maxTunnels caps concurrent CONNECT tunnels so a hostile agent cannot
	// exhaust the proxy's file descriptors/goroutines (it would only DoS its
	// own egress, but defense in depth).
	maxTunnels = 512
)

// reservedCIDRs are non-public / special-use ranges the proxy must never dial,
// even when an allowlisted (or attacker-controlled) domain resolves into them.
// This is the anti-SSRF backstop; Go's IsPrivate() alone misses several (CGNAT,
// benchmarking, NAT64, transition ranges), so the set is explicit.
var reservedCIDRs = []string{
	"0.0.0.0/8",       // "this host" / unspecified source
	"10.0.0.0/8",      // RFC1918
	"100.64.0.0/10",   // CGNAT / shared address space (RFC6598)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local (cloud metadata 169.254.169.254)
	"172.16.0.0/12",   // RFC1918
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.168.0.0/16",  // RFC1918
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved (incl. 255.255.255.255 broadcast)
	"::1/128",         // IPv6 loopback
	"::/128",          // IPv6 unspecified
	"64:ff9b::/96",    // NAT64 well-known prefix (embeds IPv4)
	"2001::/32",       // Teredo (embeds IPv4)
	"2002::/16",       // 6to4 (embeds IPv4)
	"2001:db8::/32",   // documentation
	"fc00::/7",        // ULA
	"fe80::/10",       // link-local
	"fec0::/10",       // deprecated site-local
}

var reservedNets = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(reservedCIDRs))
	for _, c := range reservedCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isBlockedIP reports whether ip is non-public / special-use and must never be
// dialed. It unwraps IPv4-in-IPv6 first so a 4-in-6 encoding of a blocked v4
// address is also caught, and keeps a !IsGlobalUnicast catch-all for anything
// the explicit list misses.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if !ip.IsGlobalUnicast() { // loopback, link-local, multicast, unspecified
		return true
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Config configures a Proxy.
type Config struct {
	// Listen is the address to listen on, e.g. ":3128".
	Listen string
	// Allow lists the permitted domains. Each entry permits the domain itself
	// and all of its subdomains ("github.com" allows "api.github.com").
	// Matching is case-insensitive. IP-literal targets are always denied.
	Allow []string
	// Ports lists the permitted target ports. Empty means the default {443, 80}.
	Ports []int
	// Logf receives audit lines (already prefixed). Defaults to a no-op.
	Logf func(format string, args ...any)

	// AllowPrivate disables the resolved-address guard that rejects loopback,
	// private, and link-local targets. It exists ONLY so tests can dial
	// 127.0.0.1 httptest servers; cmd/netproxy never sets it.
	AllowPrivate bool
}

// Proxy is a filtering forward proxy. Construct with New.
type Proxy struct {
	cfg     Config
	ports   map[int]bool
	server  *http.Server
	tunnels chan struct{} // semaphore bounding concurrent CONNECT tunnels
}

// New validates cfg and returns a Proxy ready to Serve.
func New(cfg Config) (*Proxy, error) {
	if cfg.Listen == "" {
		cfg.Listen = ":3128"
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	ports := map[int]bool{}
	if len(cfg.Ports) == 0 {
		ports[443], ports[80] = true, true
	}
	for _, p := range cfg.Ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid proxy port %d", p)
		}
		ports[p] = true
	}
	p := &Proxy{cfg: cfg, ports: ports, tunnels: make(chan struct{}, maxTunnels)}
	p.server = &http.Server{
		Addr:              cfg.Listen,
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10, // proxy requests need no large headers
	}
	return p, nil
}

// Serve listens and serves until the listener fails or Close is called.
// It emits a READY audit line once the listener is bound so a supervisor can
// wait for startup.
func (p *Proxy) Serve() error {
	ln, err := net.Listen("tcp", p.cfg.Listen)
	if err != nil {
		return fmt.Errorf("egress proxy listen %s: %w", p.cfg.Listen, err)
	}
	p.cfg.Logf("%s READY listen=%s allow=%s", AuditPrefix, ln.Addr(), strings.Join(p.cfg.Allow, ","))
	err = p.server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close shuts the proxy down.
func (p *Proxy) Close() error { return p.server.Close() }

// handle dispatches CONNECT tunnels and absolute-form HTTP requests.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	// A forward proxy receives absolute-form URLs ("GET http://host/path").
	// Origin-form requests mean the client is NOT treating us as a proxy.
	if !r.URL.IsAbs() {
		p.deny(w, r, hostOnly(r.Host), "not a proxy request (set HTTP_PROXY/HTTPS_PROXY)")
		return
	}
	p.handleHTTP(w, r)
}

// checkTarget applies the allowlist to host:port and returns a deny reason,
// or "" if the target is permitted.
//
// Security: the decision is made on the NAME the client asked for, and the
// dialer separately rejects targets that resolve to internal address ranges,
// so an allowlisted domain pointed at 127.0.0.1/RFC1918 cannot be used to
// reach the proxy's own network (SSRF).
func (p *Proxy) checkTarget(hostport string) string {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		// Absolute-form HTTP URLs commonly omit the port.
		host, portStr = hostport, "80"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || !p.ports[port] {
		return fmt.Sprintf("port %s is not permitted", portStr)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "empty host"
	}
	// IP-literal targets bypass domain-based policy entirely: always deny.
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return "IP-literal targets are not permitted (allowlist is domain-based)"
	}
	for _, d := range p.cfg.Allow {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return ""
		}
	}
	return "host is not in the network.allow list"
}

// dialer returns a context dialer whose Control hook rejects connections to
// non-public addresses AFTER resolution. This is the anti-SSRF backstop: even
// if an allowlisted (or attacker-controlled) domain resolves to a loopback,
// private, or link-local address, the dial is refused.
func (p *Proxy) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: 15 * time.Second}
	if p.cfg.AllowPrivate {
		return d
	}
	d.Control = func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("egress: unparseable dial address %q", address)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("egress: dial address %q is not an IP", address)
		}
		// Runs for EVERY resolved address the dialer tries (covers multi-A and
		// DNS-rebinding), on both the CONNECT and HTTP paths.
		if isBlockedIP(ip) {
			return fmt.Errorf("egress: %s resolves to a non-public/reserved address (%s); refused", address, ip)
		}
		return nil
	}
	return d
}

// handleConnect serves an HTTPS tunnel: verify the target, dial it, reply
// 200, then copy bytes both ways with an idle deadline until either side
// closes. Concurrent tunnels are capped so a hostile client cannot exhaust
// the proxy's resources.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if reason := p.checkTarget(target); reason != "" {
		p.deny(w, r, target, reason)
		return
	}

	// Bound concurrency: refuse rather than pile up unbounded tunnels.
	select {
	case p.tunnels <- struct{}{}:
		defer func() { <-p.tunnels }()
	default:
		p.deny(w, r, target, "too many concurrent tunnels")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	upstream, err := p.dialer().DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		p.deny(w, r, target, "dial failed: "+err.Error())
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		p.deny(w, r, target, "cannot hijack client connection")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	p.cfg.Logf("%s ALLOW connect %s", AuditPrefix, target)
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Copy both ways with an idle deadline so a half-open or slowloris tunnel
	// is reaped instead of pinning sockets/goroutines forever. Closing a conn
	// unblocks the opposite copy.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); idleCopy(upstream, buf, client, upstream); closeWrite(upstream) }()
	go func() { defer wg.Done(); idleCopy(client, upstream, client, upstream); closeWrite(client) }()
	wg.Wait()
}

// idleCopy copies src->dst, refreshing the read/write deadlines on both
// connections before each chunk so an idle tunnel is torn down after
// tunnelIdle. setDeadliner lets us bump deadlines on whatever conns back the
// stream (the client side is read through a bufio.Reader).
func idleCopy(dst io.Writer, src io.Reader, a, b net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		deadline := time.Now().Add(tunnelIdle)
		_ = a.SetDeadline(deadline)
		_ = b.SetDeadline(deadline)
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

// handleHTTP forwards a plain-HTTP absolute-form request.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Host
	if reason := p.checkTarget(target); reason != "" {
		p.deny(w, r, target, reason)
		return
	}

	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHopByHop(out.Header)

	tr := &http.Transport{DialContext: p.dialer().DialContext, DisableKeepAlives: true}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		p.deny(w, r, target, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	p.cfg.Logf("%s ALLOW http %s %s", AuditPrefix, r.Method, target)
	stripHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// deny logs and rejects a request with 403. The reason goes to the audit log
// and the response body so both the session and the failing tool explain why.
func (p *Proxy) deny(w http.ResponseWriter, r *http.Request, target, reason string) {
	p.cfg.Logf("%s DENY %s %s: %s", AuditPrefix, strings.ToLower(r.Method), target, reason)
	http.Error(w, fmt.Sprintf("agentbox egress proxy: %s %s denied: %s", r.Method, target, reason), http.StatusForbidden)
}

// hostOnly trims a port from host:port if present.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// stripHopByHop removes hop-by-hop headers that must not be forwarded.
func stripHopByHop(h http.Header) {
	for _, conn := range h.Values("Connection") {
		for _, f := range strings.Split(conn, ",") {
			if f = strings.TrimSpace(f); f != "" {
				h.Del(f)
			}
		}
	}
	for _, k := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		h.Del(k)
	}
}

// closeWrite half-closes a TCP connection if possible so the peer sees EOF.
func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}
