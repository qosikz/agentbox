package netproxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckTarget(t *testing.T) {
	p, err := New(Config{Allow: []string{"github.com", "API.OpenAI.com", " pypi.org. "}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		target string
		denied bool
	}{
		// Allowed: exact and subdomain matches on permitted ports.
		{"github.com:443", false},
		{"api.github.com:443", false},
		{"GITHUB.COM:443", false},
		{"github.com.:443", false}, // trailing dot must not bypass
		{"api.openai.com:443", false},
		{"pypi.org:80", false},
		{"files.pypi.org:443", false},

		// Denied: not in the allowlist.
		{"evil.com:443", true},
		{"github.com.evil.com:443", true}, // prefix spoof
		{"notgithub.com:443", true},       // suffix without dot boundary
		{"ithub.com:443", true},

		// Denied: ports outside {443, 80}.
		{"github.com:22", true},
		{"github.com:8443", true},
		{"github.com:3128", true},

		// Denied: IP literals always.
		{"1.1.1.1:443", true},
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"192.168.147.1:443", true},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			reason := p.checkTarget(tt.target)
			if got := reason != ""; got != tt.denied {
				t.Errorf("checkTarget(%q) denied=%v (reason %q), want denied=%v", tt.target, got, reason, tt.denied)
			}
		})
	}
}

// startProxy runs a Proxy on a random port and returns its URL and audit log.
func startProxy(t *testing.T, cfg Config) (*url.URL, *syncLog) {
	t.Helper()
	logbuf := &syncLog{}
	cfg.Listen = "127.0.0.1:0"
	cfg.Logf = logbuf.logf
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Bind explicitly so we know the port before Serve blocks.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	p.server.Addr = ln.Addr().String()
	go func() { _ = p.server.Serve(ln) }()
	t.Cleanup(func() { _ = p.Close() })
	u, _ := url.Parse("http://" + ln.Addr().String())
	return u, logbuf
}

type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (s *syncLog) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *syncLog) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

// localhostAsDomain rewrites an httptest URL host (127.0.0.1) to "localhost"
// so the proxy sees a domain name, not an IP literal.
func localhostAsDomain(t *testing.T, raw string) (host string, port int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := net.LookupPort("tcp", u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return "localhost", p
}

func TestConnectTunnelAllowedAndDenied(t *testing.T) {
	// TLS upstream the tunnel should reach.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tunneled-ok")
	}))
	defer upstream.Close()
	_, port := localhostAsDomain(t, upstream.URL)

	proxyURL, logbuf := startProxy(t, Config{
		Allow:        []string{"localhost"},
		Ports:        []int{port},
		AllowPrivate: true, // test-only: upstream resolves to 127.0.0.1
	})

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}

	// Allowed: CONNECT localhost:<port> succeeds end to end through TLS.
	resp, err := client.Get(fmt.Sprintf("https://localhost:%d/", port))
	if err != nil {
		t.Fatalf("allowed CONNECT failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "tunneled-ok" {
		t.Errorf("tunneled body = %q", body)
	}
	if !strings.Contains(logbuf.joined(), "ALLOW connect localhost:") {
		t.Errorf("missing ALLOW audit line; log:\n%s", logbuf.joined())
	}

	// Denied: a host outside the allowlist is refused at CONNECT time. Go's
	// client reports the proxy's 403 by its status text, "Forbidden".
	_, err = client.Get(fmt.Sprintf("https://denied.example:%d/", port))
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("denied CONNECT should fail with Forbidden, got: %v", err)
	}
	if !strings.Contains(logbuf.joined(), "DENY connect denied.example:") {
		t.Errorf("missing DENY audit line; log:\n%s", logbuf.joined())
	}
}

func TestPlainHTTPForwarding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "plain-ok %s", r.URL.Path)
	}))
	defer upstream.Close()
	_, port := localhostAsDomain(t, upstream.URL)

	proxyURL, logbuf := startProxy(t, Config{
		Allow:        []string{"localhost"},
		Ports:        []int{port},
		AllowPrivate: true,
	})
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/hello", port))
	if err != nil {
		t.Fatalf("allowed HTTP via proxy failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "plain-ok /hello" {
		t.Errorf("body = %q", body)
	}

	// Denied host: 403 from the proxy itself.
	resp, err = client.Get(fmt.Sprintf("http://denied.example:%d/", port))
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("denied HTTP status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(logbuf.joined(), "DENY get denied.example:") {
		t.Errorf("missing DENY audit line; log:\n%s", logbuf.joined())
	}
}

// The anti-SSRF guard must refuse targets that RESOLVE to private/loopback
// addresses even when the domain is allowlisted — this is the rebinding
// backstop. localhost is allowlisted here but AllowPrivate is FALSE.
func TestPrivateAddressGuardBlocksResolvedLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should-never-arrive")
	}))
	defer upstream.Close()
	_, port := localhostAsDomain(t, upstream.URL)

	proxyURL, logbuf := startProxy(t, Config{
		Allow: []string{"localhost"},
		Ports: []int{port},
		// AllowPrivate deliberately false.
	})
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/", port))
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("loopback-resolving target must be refused, got status %d", resp.StatusCode)
	}
	if !strings.Contains(logbuf.joined(), "non-public/reserved address") {
		t.Errorf("expected non-public-address denial in audit log:\n%s", logbuf.joined())
	}
}

// Origin-form requests (client not configured for a proxy) are refused with
// guidance rather than silently proxied.
func TestOriginFormRejected(t *testing.T) {
	proxyURL, _ := startProxy(t, Config{Allow: []string{"example.com"}})
	resp, err := http.Get(proxyURL.String() + "/some/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "HTTP_PROXY") {
		t.Errorf("origin-form should 403 with proxy guidance, got %d %q", resp.StatusCode, body)
	}
}

func TestReadyLine(t *testing.T) {
	_, logbuf := startProxy(t, Config{Allow: []string{"a.example"}})
	// Serve() emits READY; our test helper bypasses Serve, so emit via New-config
	// path is not exercised here — assert the prefix constant instead, then the
	// full READY behavior in the e2e (container) test.
	if AuditPrefix != "AGENTBOX-EGRESS" {
		t.Fatalf("audit prefix changed: %q — the runner's log scanner depends on it", AuditPrefix)
	}
	_ = logbuf
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.1", "172.16.0.1", "192.168.1.1", // RFC1918
		"169.254.169.254",  // cloud metadata (link-local)
		"100.64.0.1",       // CGNAT
		"100.127.255.254",  // CGNAT upper
		"192.0.0.1",        // protocol assignments
		"198.18.0.1",       // benchmarking
		"192.0.2.5",        // TEST-NET-1
		"198.51.100.5",     // TEST-NET-2
		"203.0.113.5",      // TEST-NET-3
		"240.0.0.1",        // reserved
		"0.0.0.0",          // unspecified
		"255.255.255.255",  // broadcast
		"fc00::1",          // ULA
		"fe80::1",          // link-local
		"fec0::1",          // deprecated site-local
		"64:ff9b::7f00:1",  // NAT64 (embeds 127.0.0.1)
		"2002:7f00:1::",    // 6to4
		"2001::1",          // Teredo
		"::ffff:10.0.0.1",  // IPv4-mapped private
		"::ffff:127.0.0.1", // IPv4-mapped loopback
	}
	for _, s := range blocked {
		if ip := net.ParseIP(s); !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want true (must be refused)", s)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "140.82.121.3", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if ip := net.ParseIP(s); isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = true, want false (public address)", s)
		}
	}
	if !isBlockedIP(nil) {
		t.Error("isBlockedIP(nil) must be true (fail closed)")
	}
}
