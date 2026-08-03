package runner

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestModelEndpointHostPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{name: "unset uses the public API", env: nil, want: "api.anthropic.com:443"},
		{name: "empty is treated as unset", env: []string{"ANTHROPIC_BASE_URL="}, want: "api.anthropic.com:443"},
		{
			name: "explicit base URL wins",
			env:  []string{"ANTHROPIC_BASE_URL=https://gateway.internal:8443/v1"},
			want: "gateway.internal:8443",
		},
		{
			name: "http default port",
			env:  []string{"ANTHROPIC_BASE_URL=http://gateway.internal"},
			want: "gateway.internal:80",
		},
		{
			name: "unparseable base URL falls back rather than failing the step",
			env:  []string{"ANTHROPIC_BASE_URL=://nonsense"},
			want: "api.anthropic.com:443",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelEndpointHostPort(tc.env); got != tc.want {
				t.Fatalf("modelEndpointHostPort() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightModelEgressReachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	host, _, _ := net.SplitHostPort(listener.Addr().String())
	if err := preflightModelEgress(context.Background(), listener.Addr().String(), func(context.Context, string) ([]string, error) {
		return []string{host}, nil
	}); err != nil {
		t.Fatalf("reachable endpoint returned %v", err)
	}
}

// A deny-all egress NetworkPolicy drops the DNS query itself, because cluster
// DNS is a separate egress rule from the model endpoint. That must be reported
// as a DNS failure, not a connect failure -- they are fixed by different rules.
func TestPreflightModelEgressNamesDNSSeparately(t *testing.T) {
	err := preflightModelEgress(context.Background(), "api.anthropic.com:443", func(context.Context, string) ([]string, error) {
		return nil, errors.New("i/o timeout")
	})
	if err == nil {
		t.Fatal("unresolvable endpoint returned no error")
	}
	for _, want := range []string{"api.anthropic.com", "cannot be resolved", "cluster DNS", "hermeticEgressTo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("DNS failure message lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "cannot be reached") {
		t.Errorf("DNS failure misreported as a connect failure: %v", err)
	}
}

func TestPreflightModelEgressNamesConnectFailure(t *testing.T) {
	// Port 1 on the loopback interface has nothing listening, so the dial
	// fails immediately without depending on outbound network access.
	err := preflightModelEgress(context.Background(), "127.0.0.1:1", func(context.Context, string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	})
	if err == nil {
		t.Fatal("unreachable endpoint returned no error")
	}
	for _, want := range []string{"127.0.0.1:1", "cannot be reached", "hermeticEgressTo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("connect failure message lacks %q: %v", want, err)
		}
	}
}

// A proxied environment makes a direct probe meaningless: the CLI talks to the
// proxy, so an unreachable endpoint says nothing about whether the run works.
// Suppressing the preflight is correct; inventing a cause is not.
func TestProxyConfiguredSuppressesThePreflight(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{name: "no proxy", env: []string{"PATH=/usr/bin"}, want: false},
		{name: "https_proxy lowercase", env: []string{"https_proxy=http://proxy:3128"}, want: true},
		{name: "HTTPS_PROXY uppercase", env: []string{"HTTPS_PROXY=http://proxy:3128"}, want: true},
		{name: "ALL_PROXY", env: []string{"ALL_PROXY=socks5://proxy:1080"}, want: true},
		{name: "empty value is not a proxy", env: []string{"HTTPS_PROXY="}, want: false},
		{name: "blank value is not a proxy", env: []string{"HTTPS_PROXY=   "}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxyConfigured(tc.env); got != tc.want {
				t.Fatalf("proxyConfigured(%v) = %t, want %t", tc.env, got, tc.want)
			}
		})
	}
}

// The whole point of the preflight is that it fails in seconds instead of
// hanging until the model client's own timeout, which cost a live debugging
// session at roughly five minutes.
func TestPreflightModelEgressIsBounded(t *testing.T) {
	if modelEgressPreflightTimeout > 30*time.Second {
		t.Fatalf("preflight timeout %v defeats the purpose; it must be far below the model client timeout", modelEgressPreflightTimeout)
	}
	start := time.Now()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation and
	// guaranteed not to be routed, so the dial blocks until the bound expires.
	err := preflightModelEgress(context.Background(), "203.0.113.1:443", func(context.Context, string) ([]string, error) {
		return []string{"203.0.113.1"}, nil
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("blackholed endpoint returned no error")
	}
	if elapsed > modelEgressPreflightTimeout+5*time.Second {
		t.Fatalf("preflight took %v, want it bounded near %v", elapsed, modelEgressPreflightTimeout)
	}
}
