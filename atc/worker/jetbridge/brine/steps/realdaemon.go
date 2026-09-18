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
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// Runs the production artifact-daemon in a subprocess with an owned storage
// root and a free port. HTTP and HTTPS fixtures share this lifecycle.
//
// Other families still contain HTTP stand-ins; those cannot verify the real
// daemon's registration, file-serving, tar or authentication behavior.
//
// The binary also supports --kubeconfig, --listen-address and a filesystem
// durable store for multi-daemon fixtures. Real discovery additionally needs
// reachable addresses that the Kubernetes EndpointSlice API accepts; envtest
// itself does not run daemon pods.

type realDaemon struct {
	Root string // the storage path this daemon serves
	URL  string // http(s)://127.0.0.1:<port>
	cmd  *exec.Cmd
	done chan error
}

var (
	daemonBinOnce sync.Once
	daemonBinPath string
	daemonBinErr  error
)

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

// artifactDaemonBinary builds once per process, not once per scenario.
func artifactDaemonBinary() (string, error) {
	daemonBinOnce.Do(func() {
		// The private-network runner builds outside the namespace, where
		// dependency downloads are possible. Direct runs retain lazy builds.
		if binary := os.Getenv("BRINE_ARTIFACT_DAEMON_BINARY"); binary != "" {
			if !filepath.IsAbs(binary) {
				daemonBinErr = fmt.Errorf("prebuilt artifact-daemon path must be absolute: %q", binary)
				return
			}
			info, err := os.Stat(binary)
			if err != nil {
				daemonBinErr = fmt.Errorf("prebuilt artifact-daemon: %w", err)
				return
			}
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				daemonBinErr = fmt.Errorf("prebuilt artifact-daemon is not an executable file: %q", binary)
				return
			}
			daemonBinPath = binary
			return
		}
		dir, err := os.MkdirTemp("", "brine-artifact-daemon-*")
		if err != nil {
			daemonBinErr = err
			return
		}
		bin := filepath.Join(dir, "artifact-daemon")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/artifact-daemon")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			daemonBinErr = fmt.Errorf("build artifact-daemon: %w\n%s", err, out)
			return
		}
		daemonBinPath = bin
	})
	return daemonBinPath, daemonBinErr
}

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

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startRealDaemon brings up one daemon and waits until it answers.
func startRealDaemon(extraArgs ...string) (*realDaemon, error) {
	return startRealDaemonWithClient("http", http.DefaultClient, extraArgs...)
}

// TLS callers supply an independently configured, verifying client. Readiness
// must complete HTTPS and receive healthz's 200; the TLS listener's HTTP 400
// is not evidence that a correctly configured TLS daemon is ready.
func startRealDaemonWithClient(scheme string, client *http.Client, extraArgs ...string) (*realDaemon, error) {
	return startConfiguredDaemon(scheme, client, daemonOptions{}, extraArgs...)
}

// A real peer pair binds two actual addresses at the same DaemonSet port.
// All callers share readiness, process ownership and filesystem cleanup.
type daemonOptions struct {
	Host string
	Port int
	Env  []string
}

func startConfiguredDaemon(scheme string, client *http.Client, options daemonOptions, extraArgs ...string) (_ *realDaemon, err error) {
	bin, err := artifactDaemonBinary()
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "brine-daemon-root-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(root)
		}
	}()
	if err := os.MkdirAll(filepath.Join(root, "steps"), 0o755); err != nil {
		return nil, err
	}
	port := options.Port
	if port == 0 {
		port, err = freePort()
		if err != nil {
			return nil, err
		}
	}
	host := options.Host
	if host == "" {
		host = "127.0.0.1"
	} else {
		extraArgs = append(extraArgs, "--listen-address", host)
	}

	args := append([]string{
		"--port", fmt.Sprint(port),
		"--storage-path", root,
	}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), options.Env...)
	// The daemon logs to stderr; keep it off the event stream, which is stdout.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start artifact-daemon: %w", err)
	}

	d := &realDaemon{Root: root, URL: fmt.Sprintf("%s://%s:%d", scheme, host, port), cmd: cmd}

	// Readiness: a route that answers even with nothing stored. A daemon that
	// died on startup must be reported as that, not as a scenario failure
	// somewhere later.
	// A daemon that dies at boot has to be reported AS THAT. The first version
	// of this loop tested cmd.ProcessState, which exec.Cmd populates only in
	// Wait/Run — nothing here calls either, so it was nil on every iteration
	// and the guard could not fire. A misconfigured daemon would have been
	// reported twenty seconds later as "did not answer", hiding its exit code
	// and the reason. Waiting in a goroutine makes the death observable.
	died := make(chan error, 1)
	d.done = died
	go func() {
		died <- cmd.Wait()
		close(died)
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-died:
			return nil, fmt.Errorf("artifact-daemon exited during startup: %w", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.URL+"/healthz", nil)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return d, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = d.stop()
	return nil, fmt.Errorf("artifact-daemon did not answer within 20s")
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
		// The goroutine started in startRealDaemon owns Wait; calling it here
		// too would race for the same exit status.
		_ = d.cmd.Process.Kill()
		select {
		case <-d.done:
		case <-time.After(20 * time.Second):
			return fmt.Errorf("artifact-daemon did not exit after kill")
		}
	}
	if d.Root != "" {
		return os.RemoveAll(d.Root)
	}
	return nil
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
				rec.RegisterDisposer(func() { _ = d.stop() })
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
