package runner

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultModelHostPort is where the claude CLI goes when ANTHROPIC_BASE_URL
	// is unset, which is the normal case for an agent pod.
	defaultModelHostPort = "api.anthropic.com:443"

	// modelEgressPreflightTimeout bounds the whole reachability probe.
	//
	// The number that matters is the one it replaces. Hermetic pods run under a
	// deny-all egress NetworkPolicy, and with networkPolicy.hermeticEgressTo
	// unset the first model call simply hung until the client gave up roughly
	// five minutes later with a bare "Request timed out" -- no indication that
	// the network, rather than the model or the prompt, was at fault. That cost
	// a live debugging session on 2026-08-01. Failing in seconds with the
	// actual cause is the entire point, so this must stay far below any model
	// client timeout.
	modelEgressPreflightTimeout = 10 * time.Second
)

// hostResolver is the DNS seam, so the failure modes can be tested without
// depending on the test host's own network.
type hostResolver func(ctx context.Context, host string) ([]string, error)

// modelEndpointHostPort derives the host:port the claude CLI will contact.
//
// A malformed ANTHROPIC_BASE_URL falls back to the public endpoint rather than
// failing: this function exists to make a preflight possible, and refusing to
// run the step over an unparseable override would turn a diagnostic aid into a
// new way to break a run. The CLI itself owns validating its own configuration.
func modelEndpointHostPort(env []string) string {
	base := ""
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && name == "ANTHROPIC_BASE_URL" {
			base = strings.TrimSpace(value)
		}
	}
	if base == "" {
		return defaultModelHostPort
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" {
		return defaultModelHostPort
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
		if parsed.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

// proxyConfigured reports whether the environment routes outbound HTTP through
// a proxy.
//
// When it does, the CLI connects to the proxy rather than to the model
// endpoint, so probing the endpoint directly proves nothing and would fail for
// a reason that does not affect the run. A preflight that invents a cause is
// worse than no preflight, so this suppresses it entirely rather than guessing
// at the proxy's own reachability.
func proxyConfigured(env []string) bool {
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.ToUpper(name) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY":
			return true
		}
	}
	return false
}

// preflightModelEgress proves the model endpoint is reachable before the CLI
// is invoked, and reports which of the two independent egress rules is missing.
//
// DNS and the endpoint itself are separate rules in the chart's
// networkPolicy.hermeticEgressTo -- cluster DNS on port 53, and the model
// endpoint on 443 -- so they are reported separately. Collapsing them into one
// "network unreachable" message would send an operator to edit the wrong rule.
//
// This deliberately stops at TCP. It does not speak TLS or HTTP and sends no
// credential, so it costs no tokens, needs no valid key, and cannot fail for
// any reason other than the one it is testing for. Authentication and model
// availability remain the CLI's business.
func preflightModelEgress(ctx context.Context, hostPort string, resolve hostResolver) error {
	ctx, cancel := context.WithTimeout(ctx, modelEgressPreflightTimeout)
	defer cancel()

	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return fmt.Errorf("model endpoint %q is not a valid host:port: %w", hostPort, err)
	}
	if net.ParseIP(host) == nil {
		if _, err := resolve(ctx, host); err != nil {
			return fmt.Errorf(
				"model endpoint %s cannot be resolved (%v). In a hermetic pod this is cluster DNS being denied: "+
					"the chart's networkPolicy.hermeticEgressTo must allow UDP/TCP 53 to kube-dns", host, err)
		}
	}

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return fmt.Errorf(
			"model endpoint %s cannot be reached (%v). In a hermetic pod this is egress being denied: "+
				"the chart's networkPolicy.hermeticEgressTo must allow TCP to it", hostPort, err)
	}
	return conn.Close()
}

func defaultHostResolver(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// modelEgressPreflight is the seam Run calls.
//
// It exists so the runner's own tests stay hermetic. Without it, every test
// that drives Run would reach for api.anthropic.com and the suite would pass
// or fail on whether the machine running it has outbound network -- turning a
// unit suite into an integration one, which is the same class of hidden
// environmental dependency that let the missing-git defect ship.
var modelEgressPreflight = func(ctx context.Context, hostPort string) error {
	return preflightModelEgress(ctx, hostPort, defaultHostResolver)
}
