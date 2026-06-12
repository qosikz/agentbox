// Command netproxy is AgentBox's egress-enforcement proxy. It is cross-compiled
// as a static linux binary, embedded into the agentbox CLI, and run in a
// sidecar container as the only path out of the sandbox network. It is not
// meant to be invoked by users directly.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/qosikz/agentbox/internal/netproxy"
)

func main() {
	listen := flag.String("listen", ":3128", "address to listen on")
	allow := flag.String("allow", "", "comma-separated domain allowlist (each entry covers its subdomains)")
	flag.Parse()

	var domains []string
	for _, d := range strings.Split(*allow, ",") {
		if d = strings.TrimSpace(d); d != "" {
			domains = append(domains, d)
		}
	}

	// Audit lines go to stdout so the container runtime captures them and the
	// AgentBox CLI can fold denials into the session's policy events.
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	p, err := netproxy.New(netproxy.Config{
		Listen: *listen,
		Allow:  domains,
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
