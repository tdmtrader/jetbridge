package jetbridge

// A REAL output daemon, as a process, for the ATC-side control specs.
//
// A double could be made to refuse. What it cannot do is say the answer is
// RIGHT: half of what these operations do is a durable record on a node's
// filesystem that no response shows, and the ordering this phase is about --
// gate before record, start before child, outcome before result -- is exactly
// the half a double would invent. So these specs run `cmd/hangar-output-daemon`
// itself, one process per spec with its own control directory on a free port.
//
// No bucket is involved. The control API touches none: it admits executions,
// records starts and outcomes, holds sources and issues writer tickets, and
// every one of those is a write to the node's own ledger. The daemon is pointed
// at an endpoint nothing answers on, which is honest -- if a control route ever
// reaches for the bucket, these specs fail rather than passing against a store
// somebody stubbed.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

const (
	harnessNodeUID = "harness-node-1"
	harnessEpoch   = executioncontrol.ActivationEpoch(7)
)

type outputDaemonHarness struct {
	Endpoint string
	StepsDir string
	Client   *OutputControlClient
	Minter   *executioncontrol.CapabilityMinter

	cmd *exec.Cmd
}

var (
	outputDaemonBuild    sync.Once
	outputDaemonBinary   string
	outputDaemonBuildErr error
)

// buildOutputDaemon builds the binary once per test process. It is ~10s cold
// and instant warm, which is why it is not per spec.
func buildOutputDaemon() (string, error) {
	outputDaemonBuild.Do(func() {
		dir, err := os.MkdirTemp("", "jetbridge-output-daemon-*")
		if err != nil {
			outputDaemonBuildErr = err

			return
		}
		binary := filepath.Join(dir, "hangar-output-daemon")
		build := exec.Command("go", "build", "-o", binary, "./cmd/hangar-output-daemon")
		build.Dir = repositoryRoot()
		if out, err := build.CombinedOutput(); err != nil {
			outputDaemonBuildErr = fmt.Errorf("building the output daemon: %w\n%s", err, out)

			return
		}
		outputDaemonBinary = binary
	})

	return outputDaemonBinary, outputDaemonBuildErr
}

func repositoryRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "hangar-output-daemon")); err == nil {
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

// startOutputDaemon brings up one daemon with its own ledger and returns a
// client already bound to it.
func startOutputDaemon() (*outputDaemonHarness, error) {
	binary, err := buildOutputDaemon()
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "jetbridge-output-control-*")
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"control", "steps", "scratch"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	receiptKey, err := writeHarnessEd25519(dir, "receipt.pem")
	if err != nil {
		return nil, err
	}
	controlKey, err := writeHarnessEd25519(dir, "control.pem")
	if err != nil {
		return nil, err
	}
	secret := make([]byte, executioncontrol.CapabilityKeyBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	capabilityKey := filepath.Join(dir, "capability.key")
	if err := os.WriteFile(capabilityKey, secret, 0o600); err != nil {
		return nil, err
	}

	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)

	daemon := exec.Command(binary,
		// The endpoint is deliberately one nothing answers on. No control
		// route needs the bucket, and a route that reached for it would fail
		// here rather than pass against a stub.
		"--output-endpoint", "http://127.0.0.1:1",
		"--output-bucket", "jetbridge-harness-output",
		"--output-prefix", "harness/one",
		"--output-tenant", "harness",
		"--receipt-key-id", "harness-receipt-1",
		"--receipt-key-file", receiptKey,
		"--control-key-id", "harness-control-1",
		"--control-key-file", controlKey,
		"--capability-key", capabilityKey,
		"--node-uid", harnessNodeUID,
		"--activation-epoch", fmt.Sprint(uint64(harnessEpoch)),
		"--control-dir", filepath.Join(dir, "control"),
		"--steps-dir", filepath.Join(dir, "steps"),
		"--scratch-dir", filepath.Join(dir, "scratch"),
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
	)
	transport := &http.Client{Timeout: 10 * time.Second}
	daemon.Stdout, daemon.Stderr = os.Stderr, os.Stderr
	if err := daemon.Start(); err != nil {
		return nil, err
	}

	if err := waitForReady(transport, endpoint); err != nil {
		_ = daemon.Process.Kill()

		return nil, err
	}

	minter, err := executioncontrol.NewCapabilityMinter(secret, time.Minute, time.Now)
	if err != nil {
		return nil, err
	}

	return &outputDaemonHarness{
		Endpoint: endpoint,
		StepsDir: filepath.Join(dir, "steps"),
		Minter:   minter,
		Client:   NewOutputControlClient(endpoint, transport, minter, harnessEpoch),
		cmd:      daemon,
	}, nil
}

func (harness *outputDaemonHarness) Stop() {
	if harness == nil || harness.cmd == nil || harness.cmd.Process == nil {
		return
	}
	_ = harness.cmd.Process.Kill()
	_, _ = harness.cmd.Process.Wait()
}

// ForNode makes the harness its own OutputControlResolver: one node, one
// daemon, which is what a spec with one Pod has.
func (harness *outputDaemonHarness) ForNode(_ context.Context, _ string) (OutputControl, error) {
	return harness.Client, nil
}

func waitForReady(client *http.Client, endpoint string) error {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("readyz answered %d", response.StatusCode)
		} else {
			last = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	return fmt.Errorf("the output daemon never became ready: %w", last)
}

func writeHarnessEd25519(dir, name string) (string, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return "", err
	}

	return path, nil
}

// freeLocalPort picks a port the daemon can bind. --listen 127.0.0.1:0 would
// have the daemon choose one, but then the endpoint would have to be scraped
// out of its stdout, and a fixture that parses a log line is a fixture that
// breaks when the banner changes.
func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port, nil
}
