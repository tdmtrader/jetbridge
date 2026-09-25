package main_test

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The daemon's own port is mTLS, and Prometheus holds neither a client
// certificate nor the daemon's CA, so for as long as /metrics was served only
// there nothing scraped it: count({__name__=~"artifact_daemon_.*"}) was empty
// on the home cluster while every series was being published. --metrics-port
// is the listener a scraper can reach.
//
// The binary runs for real with the chart's TLS flags, so this proves the
// thing the chart relies on: the metrics port answers plain HTTP while the
// daemon's port stays mTLS, and it answers /metrics and nothing else.
func TestMetricsPortServesPlainHTTPBesideTheMTLSPort(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	dir := t.TempDir()
	fix := newTLSTestFixture(t)

	bin := filepath.Join(dir, "artifact-daemon")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "resolve.key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}

	port, metricsPort := freePort(t), freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	daemon := exec.CommandContext(ctx, bin,
		"--storage-path="+filepath.Join(dir, "storage"),
		"--listen-address=127.0.0.1",
		"--port="+strconv.Itoa(port),
		"--metrics-port="+strconv.Itoa(metricsPort),
		"--mirror-replicas=0",
		"--resolve-capability-key="+keyPath,
		"--tls-cert="+fix.ServerCertPath,
		"--tls-key="+fix.ServerKeyPath,
		"--tls-ca-cert="+fix.CACertPath,
	)
	var output strings.Builder
	daemon.Stdout, daemon.Stderr = &output, &output
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = daemon.Wait()
	})

	metricsURL := "http://127.0.0.1:" + strconv.Itoa(metricsPort)
	var body string
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(metricsURL + "/metrics")
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /metrics on the metrics port: %d\n%s", resp.StatusCode, raw)
			}
			body = string(raw)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics port never answered: %v\ndaemon output:\n%s", err, output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(body, "artifact_daemon_peer_fetch_total") {
		t.Errorf("metrics port served no artifact_daemon_ families:\n%s", body)
	}

	// Anything else on the metrics port would be a daemon route with the mTLS
	// check taken off. /healthz is exempt on the TLS port and still must not
	// answer here: the listener is for one caller and one question.
	for _, path := range []string{"/healthz", "/artifacts/steps/handle/out", "/resolve"} {
		resp, err := http.Get(metricsURL + path)
		if err != nil {
			t.Fatalf("GET %s on the metrics port: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("metrics port answered %s with %d; it must serve /metrics only", path, resp.StatusCode)
		}
	}

	// The daemon's own port is still TLS: a plain-HTTP scraper gets 400, which
	// is the failure the separate listener exists to avoid.
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/metrics")
	if err != nil {
		t.Fatalf("GET plain /metrics on the daemon port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP on the daemon port got %d; want 400 from a TLS listener", resp.StatusCode)
	}
	mtls := fix.clientWithMTLS(t)
	resp, err = mtls.Get("https://127.0.0.1:" + strconv.Itoa(port) + "/healthz")
	if err != nil {
		t.Fatalf("mTLS /healthz on the daemon port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("mTLS /healthz on the daemon port got %d", resp.StatusCode)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
