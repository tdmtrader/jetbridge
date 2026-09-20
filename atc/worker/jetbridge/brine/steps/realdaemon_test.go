package steps

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
	// And it says WHY, in the daemon's own words. "exit status 1" was the whole
	// report until the launcher stopped sending both of the daemon's streams to
	// /dev/null, and an exit code is not a reason.
	if !strings.Contains(err.Error(), "not-a-real-store") {
		t.Fatalf("the daemon's own account of its refusal is missing from: %v", err)
	}
}

// A daemon that is killed takes its children with it.
//
// The subject is the production stop(), started the production way: a shell
// that forks a long sleep and stays alive itself is the shape of a daemon with
// a worker process, and before Setpgid the sleep outlived the kill.
func TestStoppingADaemonKillsWhatItSpawned(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 & echo $!; wait")
	output := newDaemonOutput()
	cmd.Stdout, cmd.Stderr = output, output
	inOwnProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a stand-in daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()

	child := 0
	// A spec about not leaking processes must not leak one when it fails: every
	// exit from here, including t.Fatalf, goes past this.
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if line := strings.TrimSpace(output.tail()); line != "" {
			child, _ = strconv.Atoi(line)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child <= 0 {
		t.Fatalf("the stand-in daemon never reported its child; it said %q", output.tail())
	}
	if !processAlive(child) {
		t.Fatalf("the child %d was not running before the kill", child)
	}

	daemon := &realDaemon{cmd: cmd, done: done, output: output}
	if err := daemon.stop(); err != nil {
		t.Fatalf("stopping the daemon: %v", err)
	}

	// The kill is delivered to the group synchronously; give the kernel the
	// moment it needs to reap, rather than asserting on the same instruction.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if !processAlive(child) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(child, syscall.SIGKILL)
	t.Fatalf("the child process %d outlived the daemon it was spawned by", child)
}

// processAlive is the test's own liveness question, deliberately not the
// adapterProcessAlive the sweep uses: a spec that asked the same helper the
// subject asks would pass on a helper that always said no.
func processAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// A daemon's dying words reach the error that reports its death.
func TestTheCaptureKeepsTheEndOfWhatADaemonSaid(t *testing.T) {
	output := newDaemonOutput()
	if report := output.report(); !strings.Contains(report, "wrote nothing") {
		t.Errorf("a silent daemon is reported as %q", report)
	}

	fmt.Fprint(output, "listen tcp 127.0.0.1:7780: bind: address already in use\n")
	if tail := output.tail(); !addressInUse(tail) {
		t.Errorf("the capture does not carry the failure: %q", tail)
	}
	if report := output.report(); !strings.Contains(report, "address already in use") ||
		strings.Contains(report, "last ") {
		t.Errorf("a short capture is reported as %q; nothing was dropped", report)
	}

	// More than it keeps, written in pieces, the way two io.Copy goroutines
	// feed it: the END has to survive, because that is where a process says
	// why it is dying.
	for i := 0; i < 400; i++ {
		fmt.Fprintf(output, "%0512d\n", i)
	}
	fmt.Fprint(output, "the last thing it said\n")

	tail := output.tail()
	if len(tail) > daemonOutputBytes {
		t.Errorf("the capture kept %d bytes; it is bounded at %d", len(tail), daemonOutputBytes)
	}
	if !strings.HasSuffix(tail, "the last thing it said\n") {
		t.Errorf("the capture dropped the end rather than the beginning: %q", tail[max(0, len(tail)-80):])
	}
	if report := output.report(); !strings.Contains(report, fmt.Sprintf("last %d bytes", daemonOutputBytes)) {
		t.Errorf("an elided capture does not say so: %q", report[:min(120, len(report))])
	}
}

// addressInUse matches what THIS kernel says, not what a comment remembers it
// saying.
func TestAddressInUseMatchesTheRealRefusal(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer held.Close()

	_, refusal := net.Listen("tcp", held.Addr().String())
	if refusal == nil {
		t.Fatal("the kernel let two listeners hold one address and port")
	}
	if !addressInUse(refusal.Error()) {
		t.Errorf("the collision this harness retries on is not recognised: %q", refusal)
	}
	if addressInUse("artifact-daemon: --tls-cert: no such file or directory") {
		t.Error("a daemon that failed for its own reason would be retried as a port collision")
	}
}

// The port a fixture is given is free where the daemon will bind it.
func TestASharedPortIsFreeOnEveryAddressItWillBeBoundOn(t *testing.T) {
	if _, err := freePortForHosts(); err == nil {
		t.Error("a shared port was composed for no addresses at all")
	}

	port, err := freePortForHosts("127.0.0.1")
	if err != nil {
		t.Fatalf("finding a port free on loopback: %v", err)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("the port reported free could not be bound: %v", err)
	}
	_ = l.Close()

	// The same address twice can never satisfy the question, and the answer is
	// an error rather than a port that only one of the two daemons can bind.
	if port, err := freePortForHosts("127.0.0.1", "127.0.0.1"); err == nil {
		t.Errorf("port %d was reported free on one address held twice over", port)
	}
}

// A shared-port group whose port is taken by a REAL listener between the probe
// and the bind comes up anyway, on a different port, with nothing for the
// scenario to see.
//
// The listener is another artifact daemon, actually holding the port, because
// the failure being reproduced is the kernel refusing the second bind -- which
// a stand-in for the occupant could only be described as doing.
func TestASharedPortGroupMovesOffAPortSomethingElseTook(t *testing.T) {
	// Bound to the specific address, not the wildcard: BSD lets a specific
	// bind share a port with a wildcard one, so a wildcard occupant would not
	// refuse the second daemon at all -- and the fixtures under test bind
	// specific addresses.
	occupant, err := startConfiguredDaemon("http", http.DefaultClient,
		daemonOptions{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer occupant.stop()
	taken, err := portOf(occupant.URL)
	if err != nil {
		t.Fatal(err)
	}

	// The first attempt is told to use the port the occupant is on -- the
	// state the fixtures reach when a parallel adapter wins the race for the
	// port freePortForHosts just handed over.
	attempts := 0
	daemons, port, err := startSharedPortDaemons([]string{"127.0.0.1"},
		func(_ int, host string, offered int) (*realDaemon, error) {
			attempts++
			if attempts == 1 {
				offered = taken
			}
			return startConfiguredDaemon("http", http.DefaultClient,
				daemonOptions{Host: host, Port: offered})
		})
	if err != nil {
		t.Fatalf("a group that lost its port to a real listener failed instead of moving: %v", err)
	}
	for _, d := range daemons {
		defer d.stop()
	}
	if attempts != 2 {
		t.Errorf("attempts to start the group: got %d, want 2", attempts)
	}
	if port == taken {
		t.Errorf("the group came back on the port the occupant holds (%d)", port)
	}
	if len(daemons) != 1 {
		t.Fatalf("daemons started: got %d, want 1", len(daemons))
	}
	if got, err := portOf(daemons[0].URL); err != nil || got != port {
		t.Errorf("the daemon is on port %d (%v), but the group reported %d", got, err, port)
	}

	// Both are real daemons answering on their own ports: the retry neither
	// disturbed the occupant nor left a half-started daemon behind.
	for name, url := range map[string]string{"occupant": occupant.URL, "moved": daemons[0].URL} {
		if err := healthzReady(http.DefaultClient)(url); err != nil {
			t.Errorf("the %s daemon does not answer: %v", name, err)
		}
	}
}

// A caller-named port that is taken is reported as such, so the group above can
// tell it apart from a daemon that refused to start for its own reason.
func TestATakenCallerPortIsReportedAsACollision(t *testing.T) {
	// Bound to the specific address, not the wildcard: BSD lets a specific
	// bind share a port with a wildcard one, so a wildcard occupant would not
	// refuse the second daemon at all -- and the fixtures under test bind
	// specific addresses.
	occupant, err := startConfiguredDaemon("http", http.DefaultClient,
		daemonOptions{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer occupant.stop()
	taken, err := portOf(occupant.URL)
	if err != nil {
		t.Fatal(err)
	}

	d, err := startConfiguredDaemon("http", http.DefaultClient,
		daemonOptions{Host: "127.0.0.1", Port: taken})
	if err == nil {
		_ = d.stop()
		t.Fatal("a daemon started on a port another daemon holds")
	}
	if !errors.Is(err, errAddressInUse) {
		t.Errorf("a port collision is not reported as one: %v", err)
	}
}

func portOf(rawURL string) (int, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(parsed.Port())
}
