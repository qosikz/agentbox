// Command netproxy is Andbo's egress-enforcement proxy. It is cross-compiled
// as a static linux binary, embedded into the andbo CLI, and run in a
// sidecar container as the only path out of the sandbox network. It is not
// meant to be invoked by users directly.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/qosikz/andbo/internal/netproxy"
)

func main() {
	listen := flag.String("listen", ":3128", "address to listen on")
	allow := flag.String("allow", "", "comma-separated domain allowlist (each entry covers its subdomains)")
	portsStr := flag.String("ports", "", "comma-separated extra egress ports (empty => default 80,443)")
	flag.Parse()

	var domains []string
	for _, d := range strings.Split(*allow, ",") {
		if d = strings.TrimSpace(d); d != "" {
			domains = append(domains, d)
		}
	}

	var ports []int
	for _, ps := range strings.Split(*portsStr, ",") {
		if ps = strings.TrimSpace(ps); ps == "" {
			continue
		}
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(os.Stderr, "netproxy: invalid -ports value %q (expected 1-65535)\n", ps)
			os.Exit(1)
		}
		ports = append(ports, n)
	}

	// Audit lines go to stdout so the container runtime captures them and the
	// Andbo CLI can fold denials into the session's policy events.
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	p, err := netproxy.New(netproxy.Config{
		Listen: *listen,
		Allow:  domains,
		Ports:  ports,
		Logf:   func(format string, args ...any) { logger.Printf(format, args...) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "netproxy:", err)
		os.Exit(1)
	}
	if err := p.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "netproxy:", err)
		os.Exit(1)
	}
}
