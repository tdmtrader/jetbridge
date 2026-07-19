package main_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServeModeSmoke(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}

	stubATC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stubATC.Close()

	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ATC_EXTERNAL_URL="+stubATC.URL,
		"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
		"AGENT_TICKET_ID=42",
		fmt.Sprintf("MCP_LISTEN_ADDR=127.0.0.1:%d", port),
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var healthy bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			healthy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("healthz never came up")
	}

	resp, err := http.Post(base+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ask_human") {
		t.Fatalf("tools/list missing ask_human: %s", raw)
	}
}

func TestServeModeFailsFastOnBadEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = []string{} // no ATC_EXTERNAL_URL
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit without required env")
	}
}

// TestServeModeFailsFastOnBadProgressInterval (D3, 2026-07-09 SSE seam delta):
// a PLATFORM_MCP_PROGRESS_INTERVAL that is invalid, <= 0, or > 30s must be a
// FATAL startup error — never clamped silently.
func TestServeModeFailsFastOnBadProgressInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	for _, bad := range []string{"bogus", "0s", "45s"} {
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"ATC_EXTERNAL_URL=http://127.0.0.1:1",
			"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
			"AGENT_TICKET_ID=42",
			"PLATFORM_MCP_PROGRESS_INTERVAL="+bad,
		)
		if err := cmd.Run(); err == nil {
			t.Fatalf("PLATFORM_MCP_PROGRESS_INTERVAL=%q: expected non-zero exit", bad)
		}
	}
}
