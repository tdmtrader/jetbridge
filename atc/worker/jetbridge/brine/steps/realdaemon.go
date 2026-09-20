package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// Runs the production artifact-daemon (and the Hangar output daemon) as a
// subprocess with an owned storage root and a free port. HTTP and HTTPS
// fixtures share this lifecycle, and so do the multi-daemon peer fixtures:
// the binary supports --kubeconfig and --listen-address, so two real daemons
// on one host can bind two loopback addresses at the same DaemonSet port and
// discover each other through a real API server.

type realDaemon struct {
	Root string // the storage path this daemon serves
	URL  string // http(s)://<host>:<port>
	cmd  *exec.Cmd
	done chan error // closed once cmd.Wait returns

	// sharedRoot marks a daemon started in somebody else's storage root, so
	// stopping it does not remove the other daemon's storage.
	sharedRoot bool

	// output is everything the daemon wrote to stdout and stderr, bounded.
	output *daemonOutput
}

// daemonOutput is a bounded capture of one daemon's stdout and stderr.
//
// Bounded because a daemon under a scenario that hammers it can log without
// limit and this is a harness, not a log store; captured at all because the
// alternative in place before it was /dev/null, and a daemon that died at boot
// took the kernel's explanation with it -- "exit status 1" was the entire
// account of a port collision, a missing flag or a bad certificate.
//
// It is written by the goroutines os/exec starts for a non-*os.File Stdout and
// Stderr -- two of them, concurrently -- and read by whichever goroutine is
// reporting the failure, so the mutex is load-bearing rather than defensive.
type daemonOutput struct {
	mu       sync.Mutex
	buf      []byte
	dropped  bool
	capacity int
}

// daemonOutputBytes is how much of a daemon's talking is kept. A boot failure
// says what it has to say in its last few lines; 8 KiB is that with room over.
const daemonOutputBytes = 8 << 10

func newDaemonOutput() *daemonOutput {
	return &daemonOutput{capacity: daemonOutputBytes}
}

// Write keeps the LAST capacity bytes, because the end of a dying process's
// output is the part that says why.
func (o *daemonOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	written := len(p)
	if len(p) > o.capacity {
		p, o.dropped = p[len(p)-o.capacity:], true
	}
	if overflow := len(o.buf) + len(p) - o.capacity; overflow > 0 {
		o.buf, o.dropped = append(o.buf[:0], o.buf[overflow:]...), true
	}
	o.buf = append(o.buf, p...)

	return written, nil
}

// tail is what the daemon said, as text.
func (o *daemonOutput) tail() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return string(o.buf)
}

// report is tail() as a sentence to hang off an error, and is empty when the
// daemon said nothing at all -- so an error does not grow a trailing colon
// introducing silence.
func (o *daemonOutput) report() string {
	tail := strings.TrimSpace(o.tail())
	if tail == "" {
		return " (it wrote nothing to stdout or stderr)"
	}

	elision := ""
	o.mu.Lock()
	if o.dropped {
		elision = fmt.Sprintf(" (last %d bytes)", o.capacity)
	}
	o.mu.Unlock()

	return fmt.Sprintf("; the daemon's own output%s:\n%s", elision, tail)
}

type builtBinary struct {
	once sync.Once
	path string
	err  error
}

var builtBinaries sync.Map // command name -> *builtBinary

// daemonBinary builds one of the repository's daemons once per process and
// reuses it. The build is ~10s cold and instant warm, which is why it is not
// per scenario.
//
// It takes the command name because there are two daemons now. The output
// plane is a SEPARATE BINARY -- Req 20 forbids it sharing a bucket with the
// cache, and a Kubernetes service account is Pod-wide, so the isolation has to
// be a second process -- and a fixture that could only build one would be a
// fixture that could not express the thing under test.
//
// BRINE_ARTIFACT_DAEMON_BINARY names a prebuilt artifact-daemon instead. The
// private-network runner builds outside the namespace, where dependency
// downloads are possible; direct runs keep the lazy build.
func daemonBinary(command string) (string, error) {
	entry, _ := builtBinaries.LoadOrStore(command, &builtBinary{})
	built := entry.(*builtBinary)
	built.once.Do(func() {
		if command == "artifact-daemon" {
			if binary := os.Getenv("BRINE_ARTIFACT_DAEMON_BINARY"); binary != "" {
				built.path, built.err = prebuiltBinary(binary)
				return
			}
		}
		// Under the adapter's own root, with the pid in its name, because a
		// 150 MB binary in the user's temp directory under a random suffix is
		// a leak nobody can attribute and nobody sweeps. See temproot.go.
		dir, err := daemonTempDir(command)
		if err != nil {
			built.err = err
			return
		}
		bin := filepath.Join(dir, command)
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+command)
		cmd.Dir = repoRoot()
		// The go tool's own work directory goes inside ours too: it removes it
		// on success and LEAVES it on a signal, and on a signal is exactly when
		// it used to be left in the user's temp directory forever.
		cmd.Env = append(os.Environ(), "TMPDIR="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			built.err = fmt.Errorf("build %s: %w\n%s", command, err, out)
			return
		}
		built.path = bin
	})
	return built.path, built.err
}

func prebuiltBinary(binary string) (string, error) {
	if !filepath.IsAbs(binary) {
		return "", fmt.Errorf("prebuilt artifact-daemon path must be absolute: %q", binary)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return "", fmt.Errorf("prebuilt artifact-daemon: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("prebuilt artifact-daemon is not an executable file: %q", binary)
	}
	return binary, nil
}

func artifactDaemonBinary() (string, error) { return daemonBinary("artifact-daemon") }

// repoRoot walks up from this package to the module root holding cmd/.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "artifact-daemon")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// freePort answers with a port that was free on 127.0.0.1 a moment ago.
//
// "A moment ago" is the whole caveat, and it is why nothing here may treat the
// answer as a reservation: bind-zero, read the port, close, hand it to a
// subprocess that binds it later is a race with every other process on the
// host, including a second adapter running this same suite in parallel. The
// daemon binds it, so the daemon is where the collision has to be survived --
// see launchDaemon, which retries on EADDRINUSE with a fresh port.
func freePort() (int, error) {
	return freePortOn("127.0.0.1")
}

// freePortOn is freePort asked about THE ADDRESS THE DAEMON WILL BIND.
//
// A port is free per (address, port) pair, not per host. The peer fixtures bind
// 10.x addresses the private-network runner sets up and used to ask 127.0.0.1
// whether the port was free -- a question about a different socket, whose
// answer said nothing about the one they were about to open.
func freePortOn(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// freePortForHosts finds one port free on EVERY address given, holding all the
// listeners open at once before it lets any of them go.
//
// The peer fixtures need a single port because that is the shape of the thing
// under test: one DaemonSet port, several node addresses. Asking 127.0.0.1 for
// it and binding 10.0.0.2 and 10.0.0.3 tested nothing; asking each address in
// turn would leave the window in which the second address's port is taken while
// the first is being checked. Holding them simultaneously closes that window
// for this process, and the daemon's EADDRINUSE retry closes what is left
// against other processes.
func freePortForHosts(hosts ...string) (int, error) {
	if len(hosts) == 0 {
		return 0, fmt.Errorf("a shared daemon port needs at least one address to be free on")
	}

	var lastErr error
	for attempt := 0; attempt < daemonPortAttempts; attempt++ {
		// The first listener STAYS OPEN while the rest are tried. Closing it to
		// learn the port and then asking about the others in turn is the window
		// this function exists to shut: each address would be free at a
		// different instant, which is not the same as one port being free on
		// all of them.
		first, err := net.Listen("tcp", net.JoinHostPort(hosts[0], "0"))
		if err != nil {
			return 0, err
		}
		port := first.Addr().(*net.TCPAddr).Port
		held, holdErr := holdPortOn(hosts[1:], port)
		for _, l := range held {
			_ = l.Close()
		}
		_ = first.Close()
		if holdErr == nil {
			return port, nil
		}
		lastErr = holdErr
	}

	return 0, fmt.Errorf("no port was free on all of %v after %d attempts: %w",
		hosts, daemonPortAttempts, lastErr)
}

// portIsBindable answers whether a listener can be opened on exactly the
// (address, port) pair the daemon is about to bind, by opening one and closing
// it again. See launchDaemon for why the answer is worth having even though it
// expires immediately.
func portIsBindable(host string, port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return l.Close()
}

// errAddressInUse marks a daemon start that died because something else was
// already on the port. It exists so a caller that CHOSE the port can tell that
// failure apart from a daemon that refused to start for its own reasons, and
// retry the only way a shared port can be retried -- see
// startSharedPortDaemons.
var errAddressInUse = errors.New("daemon port already in use")

// startSharedPortDaemons starts one daemon per address at ONE port free on all
// of them, retrying THE WHOLE GROUP on a fresh shared port when a daemon loses
// the race for it.
//
// freePortForHosts closes its listeners before any daemon binds, so the port it
// answers with is a guess about the next instant, not a reservation: a second
// adapter running this same suite in parallel probes the same addresses with
// the same code, and outside run-private-network's netns those addresses are
// just addresses on this one host. launchDaemon's own EADDRINUSE retry cannot
// rescue this case, because it would move ONE daemon of the group to a
// different port while the EndpointSlice publishes a single one -- so it
// refuses to move a caller-named port at all, and the retry has to happen out
// here, where the whole group can move together: stop every daemon of the
// failed attempt (stop() removes the root it made), pick a new shared port,
// start over.
//
// Handing the daemon an already-bound listener would close the window instead
// of surviving it, but cmd/artifact-daemon has no such flag -- --port and
// --listen-address are the whole of its listener configuration, with no
// listener-fd option to inherit one -- so detect-and-retry is what is
// available.
func startSharedPortDaemons(hosts []string,
	start func(index int, host string, port int) (*realDaemon, error)) ([]*realDaemon, int, error) {
	if len(hosts) == 0 {
		return nil, 0, fmt.Errorf("a shared-port daemon group needs at least one address")
	}

	var lastErr error
	for attempt := 0; attempt < daemonPortAttempts; attempt++ {
		port, err := freePortForHosts(hosts...)
		if err != nil {
			return nil, 0, err
		}
		var started []*realDaemon
		var failed error
		for i, host := range hosts {
			d, startErr := start(i, host, port)
			if startErr != nil {
				failed = startErr
				break
			}
			started = append(started, d)
		}
		if failed == nil {
			return started, port, nil
		}
		// Nothing of the failed attempt may survive into the next one: a
		// daemon left running would hold the address it did win, and a root
		// left behind would be storage nobody disposes.
		for _, d := range started {
			if stopErr := d.stop(); stopErr != nil {
				return nil, 0, errors.Join(failed, stopErr)
			}
		}
		if !errors.Is(failed, errAddressInUse) {
			return nil, 0, failed
		}
		lastErr = failed
	}

	return nil, 0, fmt.Errorf("no shared daemon port on %v survived %d attempts: %w",
		hosts, daemonPortAttempts, lastErr)
}

// holdPortOn opens the same port on each address, returning whatever it managed
// to open so the caller can close them however it goes.
func holdPortOn(hosts []string, port int) ([]net.Listener, error) {
	var held []net.Listener
	for _, host := range hosts {
		l, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err != nil {
			return held, err
		}
		held = append(held, l)
	}

	return held, nil
}

// daemonPortAttempts is how many ports a daemon start will try before it gives
// up and says so. A collision is rare and independent, so three is the
// difference between "flakes sometimes" and "does not flake"; the point of a
// bound at all is that a daemon which refuses to start for its OWN reason must
// not be retried forever under the name of a port collision.
const daemonPortAttempts = 3

// addressInUse reads a daemon's own dying words for the one failure this
// harness is allowed to retry.
//
// The daemon exits non-zero with the kernel's message on its stderr; it is not
// an error value this process can errors.Is, because it happened in another
// process. Matching the text is therefore the only evidence available, and it
// is matched narrowly -- a daemon that failed for any other reason is reported,
// not retried.
func addressInUse(output string) bool {
	return strings.Contains(output, "address already in use") ||
		strings.Contains(output, "Only one usage of each socket address")
}

// readDaemonHTTP checks a response from an actual daemon, without supplying
// protocol behavior. Local and live fixtures share this independent readback.
func readDaemonHTTP(ctx context.Context, client *http.Client, method, url string, body io.Reader, status int) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != status {
		return nil, fmt.Errorf("%s %s: HTTP %d, want %d: %s", method, url, response.StatusCode, status, content)
	}
	return content, errors.Join(readErr, closeErr)
}

// healthzReady is the readiness probe for a daemon that answers /healthz.
// Readiness is a 200, not a connection: a TLS listener answers a plaintext
// probe with HTTP 400, which is not evidence that the daemon is ready.
func healthzReady(client *http.Client) func(url string) error {
	return func(url string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, err := readDaemonHTTP(ctx, client, http.MethodGet, url+"/healthz", nil, http.StatusOK)
		return err
	}
}

// startRealDaemon brings up one plaintext daemon and waits until it answers.
func startRealDaemon(extraArgs ...string) (*realDaemon, error) {
	return startRealDaemonWithClient("http", http.DefaultClient, extraArgs...)
}

// startRealDaemonWithClient is startRealDaemon for a caller that owns the
// client the probe must use: a TLS daemon needs a verifying client to reach
// /healthz at all.
func startRealDaemonWithClient(scheme string, client *http.Client, extraArgs ...string) (*realDaemon, error) {
	return startConfiguredDaemon(scheme, client, daemonOptions{}, extraArgs...)
}

// startRealDaemonProbed is startRealDaemon with the caller choosing the URL
// scheme and the readiness probe. A daemon whose Hangar surface is
// mTLS-protected needs a client certificate to answer anything but /healthz —
// so the probe belongs to the fixture that knows those things.
func startRealDaemonProbed(scheme string, ready func(url string) error, extraArgs ...string) (*realDaemon, error) {
	return startNamedDaemonProbed("artifact-daemon", scheme, ready, extraArgs...)
}

// startNamedDaemonProbed is the same launcher for either daemon.
//
// The port and storage flags are the artifact daemon's spelling, so the output
// daemon -- which listens on --listen and keeps its ledgers under --control-dir
// -- passes its own and gets them through addressableArgs below rather than
// having this function learn two vocabularies.
func startNamedDaemonProbed(command, scheme string, ready func(url string) error, extraArgs ...string) (*realDaemon, error) {
	return startNamedDaemonInRoot(command, "", scheme, ready, extraArgs...)
}

// startNamedDaemonInRoot is the same launcher with the node's storage root
// chosen by the caller.
//
// The output daemon is started in the ARTIFACT daemon's root, because on a real
// node there is one hostPath and both daemons are on it: the output daemon's
// control directory lives under it, and the artifact daemon's read-only ledger
// classifier is what reads that directory before it destroys anything. Two
// roots made every "a capture holds this" question unanswerable -- the
// classifier looked in a directory the other daemon never wrote to -- and a
// guard asked over two roots can only ever answer "unmanaged".
func startNamedDaemonInRoot(command, root, scheme string, ready func(url string) error,
	extraArgs ...string) (*realDaemon, error) {
	return launchDaemon(command, root, scheme, ready, daemonOptions{}, extraArgs...)
}

// daemonOptions is what a real peer pair needs beyond the defaults: two
// actual addresses at the same DaemonSet port, and the pod identity the
// daemon reads from its environment.
type daemonOptions struct {
	Host string   // bind this address instead of 127.0.0.1
	Port int      // bind this port instead of a free one
	Env  []string // appended to the daemon's environment
}

// startConfiguredDaemon starts an artifact daemon with explicit address, port
// and environment, probing /healthz with the given client.
func startConfiguredDaemon(scheme string, client *http.Client, options daemonOptions, extraArgs ...string) (*realDaemon, error) {
	return launchDaemon("artifact-daemon", "", scheme, healthzReady(client), options, extraArgs...)
}

// launchDaemon is the one launcher every start* wrapper reaches.
//
// It owns the port when the caller did not choose one, and a port this process
// found free is not a port this process holds: between the probe closing its
// listener and the daemon opening its own, anything on the host can take it --
// most plausibly a second adapter running this same suite in parallel, which
// probes the same loopback address with the same code a millisecond apart. That
// showed up as "artifact-daemon exited during startup: exit status 1", with the
// kernel's explanation written to a stderr this function sent to /dev/null.
// Both halves are fixed here: the output is captured, and a start that died of
// EADDRINUSE is retried on a fresh port instead of being reported as a
// mysterious boot failure.
func launchDaemon(command, root, scheme string, ready func(url string) error,
	options daemonOptions, extraArgs ...string) (_ *realDaemon, err error) {
	bin, err := daemonBinary(command)
	if err != nil {
		return nil, err
	}
	shared := root != ""
	if !shared {
		root, err = daemonTempDir("root")
		if err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				_ = os.RemoveAll(root)
			}
		}()
	}
	if err := os.MkdirAll(filepath.Join(root, "steps"), 0o755); err != nil {
		return nil, err
	}
	host := options.Host
	if host == "" {
		host = "127.0.0.1"
	} else if command != "hangar-output-daemon" {
		// The output daemon has no --listen-address; addressableArgs writes the
		// address into its --listen instead.
		extraArgs = append(extraArgs, "--listen-address", host)
	}

	// A caller that named a port meant that port: the peer fixtures need two
	// daemons at ONE DaemonSet port on two addresses, and silently moving one
	// of them would leave the scenario passing while testing nothing. Only a
	// port this function chose may be rechosen.
	attempts := daemonPortAttempts
	if options.Port != 0 {
		attempts = 1
	}

	for attempt := 1; ; attempt++ {
		port := options.Port
		if port == 0 {
			port, err = freePortOn(host)
			if err != nil {
				return nil, err
			}
		}

		// Ask the kernel about THIS (address, port) pair before spending a
		// process on it. A daemon that dies of EADDRINUSE cannot be detected
		// by the readiness probe alone: /healthz is answered identically by
		// whatever is already on the port, so the corpse would look ready.
		// This is a check about the next instant, not a reservation -- the
		// daemon's own dying words below still cover the rest of the window.
		if bindErr := portIsBindable(host, port); bindErr != nil {
			if !addressInUse(bindErr.Error()) {
				return nil, bindErr
			}
			if attempt >= attempts {
				return nil, fmt.Errorf("%w: %w", errAddressInUse, bindErr)
			}
			continue
		}

		d, output, startErr := startDaemonProcess(bin, command, root, host, scheme, port, shared,
			options.Env, extraArgs)
		if startErr != nil {
			return nil, startErr
		}

		exited, readyErr := awaitDaemon(d, command, ready, output)
		if readyErr == nil {
			return d, nil
		}
		if !exited || !addressInUse(output.tail()) {
			return nil, readyErr
		}
		if attempt >= attempts {
			// Out of attempts on a port collision -- which, when the CALLER
			// named the port, is the first attempt. This function may not move
			// that port, but whoever chose it can choose another, so the
			// failure is reported wearing a marker that says which failure it
			// was. See startSharedPortDaemons.
			return nil, fmt.Errorf("%w: %w", errAddressInUse, readyErr)
		}
		// Its own root came with it if it made one; the daemon that died never
		// wrote anything but the port complaint, and the next attempt reuses
		// the directory this function already made.
	}
}

// inOwnProcessGroup puts a command in a process group of its own, with itself
// as leader, so that killing the negated pid reaches the daemon AND anything it
// spawned.
//
// A daemon killed by pid alone leaves its children running, holding the
// listening socket and the storage root: a scenario that passed, a root that
// could not be removed, and a port the next daemon could not bind. It is a
// function rather than two lines inline so the spec for stop() can start its
// subject the way production does instead of restating it.
func inOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// startDaemonProcess starts one daemon process, with its output captured and
// its own process group, and reports the handle plus that capture.
func startDaemonProcess(bin, command, root, host, scheme string, port int, shared bool,
	env []string, extraArgs []string) (*realDaemon, *daemonOutput, error) {
	args := append(addressableArgs(command, host, port, root), extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	// Both streams go to a bounded capture rather than to this process's own.
	// STDOUT IN PARTICULAR MUST NOT REACH FD 1: fd 1 is the brine adapter's
	// event-protocol pipe, and one line of daemon logging on it corrupts the
	// run's event stream. Sending them to nil (/dev/null) kept the protocol
	// safe and threw away the only account of why a daemon failed to boot.
	output := newDaemonOutput()
	cmd.Stdout, cmd.Stderr = output, output
	inOwnProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", command, err)
	}

	d := &realDaemon{Root: root, URL: fmt.Sprintf("%s://%s:%d", scheme, host, port), cmd: cmd,
		sharedRoot: shared, output: output}

	// A daemon that dies at boot has to be reported AS THAT. The first version
	// of this loop tested cmd.ProcessState, which exec.Cmd populates only in
	// Wait/Run — nothing here calls either, so it was nil on every iteration
	// and the guard could not fire. A misconfigured daemon would have been
	// reported twenty seconds later as "did not answer", hiding its exit code
	// and the reason. Waiting in a goroutine makes the death observable, and
	// stop() waits on the same channel so a killed daemon is gone before its
	// root is removed.
	died := make(chan error, 1)
	d.done = died
	go func() {
		died <- cmd.Wait()
		close(died)
	}()

	return d, output, nil
}

// awaitDaemon waits for one daemon to answer its readiness probe, and reports
// what it said if it does not.
//
// The bool distinguishes the two failures for the caller's retry: true means
// the daemon EXITED (a port collision looks like this), false means it is still
// running but never became ready, which no fresh port would fix.
func awaitDaemon(d *realDaemon, command string, ready func(url string) error,
	output *daemonOutput) (bool, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-d.done:
			// Wait has returned, so the io.Copy of the daemon's output has
			// finished too: the tail here is everything it ever said.
			return true, fmt.Errorf("%s exited during startup: %w%s", command, err, output.report())
		default:
		}
		if err := ready(d.URL); err == nil {
			return false, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = d.stop()

	return false, fmt.Errorf("%s did not answer within 20s%s", command, output.report())
}

// addressableArgs is each daemon's own spelling of "listen here, keep your
// state there". Two daemons, two flag names, one launcher.
//
// The host is passed rather than assumed: the artifact daemon takes its address
// as a separate --listen-address, but the output daemon's --listen carries
// both, and spelling 127.0.0.1 into it while the launcher had been told to bind
// something else would have put the daemon on an address nobody was probing.
func addressableArgs(command, host string, port int, root string) []string {
	if command == "hangar-output-daemon" {
		return []string{
			"--listen", net.JoinHostPort(host, fmt.Sprint(port)),
			"--control-dir", root,
			"--steps-dir", filepath.Join(root, "steps"),
			"--scratch-dir", filepath.Join(root, "scratch"),
		}
	}

	return []string{"--port", fmt.Sprint(port), "--storage-path", root}
}

// registerDaemonArtifact asks the production daemon to register a file it
// already holds. Both plaintext and mTLS fixtures use this request path.
func registerDaemonArtifact(ctx context.Context, client *http.Client, baseURL, key, path string) error {
	body := fmt.Sprintf("{\"key\":%q,\"local_path\":%q}", key, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/register", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("register %q with the daemon: %w", key, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("the daemon refused to register %q at %q: %d %s",
			key, path, resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	return nil
}

func (d *realDaemon) stop() error {
	if d.cmd != nil && d.cmd.Process != nil {
		// The goroutine started in launchDaemon owns Wait; calling it here
		// too would race for the same exit status.
		//
		// The GROUP, not the process. startDaemonProcess gave the daemon its
		// own process group with itself as leader, so the negated pid names
		// the daemon and every child it started. Killing the leader alone left
		// those children holding the listening socket and the storage root: a
		// scenario that passed, a root that could not be removed, and a port
		// that the NEXT daemon could not bind.
		if err := syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			// A group that is not ours to signal, or that has already gone:
			// fall back to the one process we certainly own.
			_ = d.cmd.Process.Kill()
		}
		if d.done != nil {
			select {
			case <-d.done:
			case <-time.After(stopWait()):
				return fmt.Errorf("daemon did not exit after kill%s", d.outputReport())
			}
		}
	}
	// A shared root belongs to whoever created it. Removing it here would take
	// the other daemon's storage with it.
	if d.Root != "" && !d.sharedRoot {
		return os.RemoveAll(d.Root)
	}
	return nil
}

// outputReport is the daemon's captured output for an error message, and is
// empty for a daemon started before the capture existed rather than nil-panicking.
func (d *realDaemon) outputReport() string {
	if d.output == nil {
		return ""
	}

	return d.output.report()
}

// NOTE: there is deliberately no RealDaemonResourceDefinition. A
// ScopeScenario resource is acquired for every scenario in the suite
// regardless of which steps declare it, so registering the daemon that way
// cost 70 seconds to serve five scenarios. The Given above starts one on
// demand instead.

// ---------------------------------------------------------------------------
// Domain state and steps
// ---------------------------------------------------------------------------

// RealDaemonState is a running daemon and the last answer it gave.
type RealDaemonState struct {
	Daemon *realDaemon
	Ctx    context.Context
	Status int
	Body   []byte
	Err    error
}

// tarEntries lists the member names of the last answer, in order.
func (s RealDaemonState) tarEntries() ([]string, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	tr := tar.NewReader(bytes.NewReader(s.Body))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the daemon's answer is not a readable tar: %w", err)
		}
		// FILES, which is what both sentences over this say. Since
		// 65e1f31228 folded the two egress tar producers into one, the
		// archive also carries a TypeDir header for every directory it
		// walks through; counting those made "the archive carries 2
		// files" fail on an archive that carries exactly the two files
		// it names. core's own daemon tests read a tar the same way,
		// collecting only tar.TypeReg members.
		if h.Typeflag != tar.TypeReg {
			continue
		}
		names = append(names, h.Name)
	}
	return names, nil
}

func (s RealDaemonState) tarMember(name string) (string, error) {
	tr := tar.NewReader(bytes.NewReader(s.Body))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("the archive has no member %q", name)
		}
		if err != nil {
			return "", err
		}
		if h.Name == name {
			body, err := io.ReadAll(tr)
			return string(body), err
		}
	}
}

func (s RealDaemonState) get(path string) RealDaemonState {
	resp, err := http.Get(s.Daemon.URL + path)
	if err != nil {
		s.Err, s.Status, s.Body = err, 0, nil
		return s
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	s.Status, s.Body, s.Err = resp.StatusCode, body, err
	return s
}

func (s RealDaemonState) postJSON(path, payload string) RealDaemonState {
	resp, err := http.Post(s.Daemon.URL+path, "application/json", strings.NewReader(payload))
	if err != nil {
		s.Err, s.Status, s.Body = err, 0, nil
		return s
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	s.Status, s.Body, s.Err = resp.StatusCode, body, err
	return s
}

// RealDaemonDefinitions drives an actual artifact-daemon process.
func RealDaemonDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Started HERE rather than as a scenario-scoped resource, and the
		// difference is 70 seconds of suite.
		//
		// brine acquires every ScopeScenario resource before EVERY scenario —
		// RequireAllForScope iterates the definitions at that scope, not the
		// ones a scenario's steps declare. A daemon registered that way is
		// therefore built, started and killed 380 times to be used 5 times.
		// Measured: the suite went from 118s to 188s. Starting it in the Given
		// that asks for one, with its kill registered on the Recorder, is lazy
		// and drains LIFO at scenario end on pass, on failure and on SIGTERM.
		brine.DefineMap[brine.Empty, RealDaemonState](
			"a real artifact daemon",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (RealDaemonState, error) {
				d, err := startRealDaemon()
				if err != nil {
					return RealDaemonState{}, err
				}
				TrackDisposer(rec, "the real artifact daemon", d.stop)
				return RealDaemonState{Daemon: d, Ctx: context.Background()}, nil
			},
		),

		// The Given a step actually performs: it writes files into its output
		// directory on the node. Nothing tells the daemon they are there.
		brine.DefineMap[RealDaemonState, RealDaemonState](
			"a step wrote {string} into its output {string}",
			func(in RealDaemonState, p brine.Params, _ *brine.Recorder) (RealDaemonState, error) {
				content, _ := p.GetString(0)
				rel, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected content and a path")
				}
				full := filepath.Join(in.Daemon.Root, "steps", rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return in, err
				}
				return in, os.WriteFile(full, []byte(content), 0o644)
			},
		),

		brine.DefineMap[RealDaemonState, RealDaemonState](
			"the ATC asks it for the artifact {string}",
			func(in RealDaemonState, p brine.Params, _ *brine.Recorder) (RealDaemonState, error) {
				key, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected an artifact key")
				}
				return in.get("/artifacts/steps/" + key), nil
			},
		),

		brine.DefineMap[RealDaemonState, RealDaemonState](
			"the ATC asks it for the registered artifact {string}",
			func(in RealDaemonState, p brine.Params, _ *brine.Recorder) (RealDaemonState, error) {
				key, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected an artifact key")
				}
				return in.get("/artifacts/" + key), nil
			},
		),

		brine.DefineMap[RealDaemonState, RealDaemonState](
			"the ATC registers {string} as living at the step output {string}",
			func(in RealDaemonState, p brine.Params, _ *brine.Recorder) (RealDaemonState, error) {
				key, _ := p.GetString(0)
				rel, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a key and a path")
				}
				full := filepath.Join(in.Daemon.Root, "steps", rel)
				return in.postJSON("/register", fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, full)), nil
			},
		),

		brine.DefineMap[RealDaemonState, RealDaemonState](
			"the ATC registers {string} as living at the absolute path {string}",
			func(in RealDaemonState, p brine.Params, _ *brine.Recorder) (RealDaemonState, error) {
				key, _ := p.GetString(0)
				path, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a key and a path")
				}
				return in.postJSON("/register", fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, path)), nil
			},
		),

		CheckThat[RealDaemonState]("the artifact arrives", func(in RealDaemonState) error {
			if in.Err != nil {
				return fmt.Errorf("the read failed: %v", in.Err)
			}
			if in.Status != http.StatusOK {
				return fmt.Errorf("expected the daemon to serve the artifact, it answered %d: %s",
					in.Status, abbrev(string(in.Body)))
			}
			return nil
		}),

		CheckInt[RealDaemonState]("the daemon answers {int}",
			"the daemon's status",
			func(in RealDaemonState) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Status, nil
			},
			func(in RealDaemonState) string { return "body: " + abbrev(string(in.Body)) }),

		CheckContains[RealDaemonState]("the refusal explains {string}",
			"the daemon's refusal",
			func(in RealDaemonState) (string, error) {
				if in.Status >= 200 && in.Status < 300 {
					return "", fmt.Errorf("expected a refusal, the daemon answered %d", in.Status)
				}
				return string(in.Body), nil
			}),

		// The claim the whole artifact contract rests on: a directory comes
		// back as a tar whose members carry the path they had on disk.
		CheckMember[RealDaemonState]("the archive carries a file at {string}",
			"the archive's members",
			func(in RealDaemonState) ([]string, error) { return in.tarEntries() }),

		CheckCount[RealDaemonState]("the archive carries {int} files",
			"files in the archive",
			func(in RealDaemonState) ([]string, error) { return in.tarEntries() }),

		CheckStringFor[RealDaemonState]("the file at {string} reads {string}",
			"the file's contents",
			func(in RealDaemonState, name string) (string, error) { return in.tarMember(name) }),
	}
}
