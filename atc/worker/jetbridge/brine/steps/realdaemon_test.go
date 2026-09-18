package steps

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDaemonRouteClosesLiveKeepAliveConnections(t *testing.T) {
	d, err := startRealDaemon()
	if err != nil {
		t.Fatal(err)
	}
	defer d.stop()
	route, err := routeToPeer("127.0.0.1:0", strings.TrimPrefix(d.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer route.Close()
	client, err := net.DialTimeout("tcp", route.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+route.Addr().String()+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(client); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || response.Close {
		t.Fatalf("real daemon did not answer over a persistent route: status=%d, read=%v, close=%v, persistent=%v",
			response.StatusCode, readErr, closeErr, !response.Close)
	}
	done := make(chan error, 1)
	go func() { done <- route.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		// Release both sockets before failing, even if route cancellation broke.
		client.Close()
		d.stop()
		t.Fatal("closing the route did not drain its live connection")
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("closed route did not close the active connection: %v", err)
	}
}

// The guard this replaces could not fire. It tested cmd.ProcessState, which
// exec.Cmd populates only inside Wait/Run — and nothing called either — so it
// was nil on every iteration of the readiness loop. A daemon that died at boot
// was reported twenty seconds later as "did not answer", with its exit code and
// its reason thrown away.
func TestStartRealDaemonReportsADaemonThatDiesAtBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the artifact-daemon binary")
	}
	// An unknown durable store is refused at startup, so the process exits
	// before it ever listens.
	d, err := startRealDaemon("--durable-store", "not-a-real-store")
	if err == nil {
		_ = d.stop()
		t.Fatal("a daemon that cannot start MUST be reported as such, not waited on")
	}
	if strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("the death was reported as a timeout, which is the bug this guards: %v", err)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("expected the failure to say the daemon exited, got: %v", err)
	}
}
