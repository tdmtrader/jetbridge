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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
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
	// PKI is set for a TLS daemon: what a real OutputSource needs to reach it.
	PKI *harnessControlPKI

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
		binary := filepath.Join(tempRoot, "hangar-output-daemon")
		build := exec.Command("go", "build", "-o", binary, "./cmd/hangar-output-daemon")
		build.Dir = repositoryRoot()
		// The go tool's own work directory goes inside this package's root, so
		// that a build killed by a signal leaves its `go-build*` where this
		// process's cleanup will find it rather than in the user's temp
		// directory forever.
		build.Env = append(os.Environ(), "TMPDIR="+tempRoot)
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
func startOutputDaemon() (*outputDaemonHarness, error) { return startOutputDaemonWith(false) }

// startTLSOutputDaemon is the same daemon with J6's three control-TLS flags set.
//
// It exists because the node-local capture-hold exemption is a CLIENT
// CERTIFICATE exemption and not a plaintext port: the one listener is wrapped
// in tls.NewListener, so an init container that dials `http://` is answered
// "Client sent an HTTP request to an HTTPS server" and holds nothing. Driving
// the generated script needs a daemon in that shape.
func startTLSOutputDaemon() (*outputDaemonHarness, error) { return startOutputDaemonWith(true) }

func startOutputDaemonWith(secure bool) (*outputDaemonHarness, error) {
	binary, err := buildOutputDaemon()
	if err != nil {
		return nil, err
	}

	// Under the package's own root, so that a daemon this harness starts takes
	// its ledger, its steps and its keys with it when the process ends. Nothing
	// removed these, and a suite that starts one per spec left one per spec.
	dir, err := os.MkdirTemp(tempRoot, "control-*")
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
	// The output read-warrant key: a THIRD key, distinct from the receipt key and
	// from the control capability key. A warrant must not be signable by anything
	// that can mint a publication receipt, and the daemon refuses a
	// configuration where two of the three are one file.
	materializeSecret := make([]byte, output.ReadWarrantKeyBytes)
	if _, err := rand.Read(materializeSecret); err != nil {
		return nil, err
	}
	materializeKey := filepath.Join(dir, "materialize.key")
	if err := os.WriteFile(materializeKey, materializeSecret, 0o600); err != nil {
		return nil, err
	}

	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)

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
		"--materialization-key-id", "harness-materialize-1",
		"--materialization-key-file", materializeKey,
		"--node-uid", harnessNodeUID,
		"--activation-epoch", fmt.Sprint(uint64(harnessEpoch)),
		"--control-dir", filepath.Join(dir, "control"),
		"--steps-dir", filepath.Join(dir, "steps"),
		"--scratch-dir", filepath.Join(dir, "scratch"),
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
	)
	transport := &http.Client{Timeout: 10 * time.Second}
	var pki *harnessControlPKI
	if secure {
		pki, err = writeHarnessControlPKI(dir)
		if err != nil {
			return nil, err
		}
		daemon.Args = append(daemon.Args,
			"--tls-cert", pki.serverCert,
			"--tls-key", pki.serverKey,
			"--tls-ca-cert", pki.caCert)
		transport = pki.client
	}
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
		PKI:      pki,
		Client:   NewOutputControlClient(endpoint, transport, minter, harnessEpoch),
		cmd:      daemon,
	}, nil
}

// harnessControlPKI is one throwaway CA, a server certificate for 127.0.0.1 and
// a client certificate for the ATC's own calls.
//
// The init container's calls present NO client certificate -- that is the
// exemption the hold route exists inside -- so the shape this mints is exactly
// production's: an authenticated control plane and an unauthenticated,
// node-local init.
type harnessControlPKI struct {
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
	client     *http.Client
}

func writeHarnessControlPKI(dir string) (*harnessControlPKI, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "harness-control-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	issue := func(name string, serial int64, server bool) (certPath, keyPath string, pair tls.Certificate, err error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", "", tls.Certificate{}, err
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		if server {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			return "", "", tls.Certificate{}, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return "", "", tls.Certificate{}, err
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		certPath = filepath.Join(dir, name+".crt")
		keyPath = filepath.Join(dir, name+".key")
		if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
			return "", "", tls.Certificate{}, err
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			return "", "", tls.Certificate{}, err
		}
		pair, err = tls.X509KeyPair(certPEM, keyPEM)

		return certPath, keyPath, pair, err
	}

	serverCert, serverKey, _, err := issue("harness-control-server", 2, true)
	if err != nil {
		return nil, err
	}
	clientCert, clientKey, clientPair, err := issue("harness-control-client", 3, false)
	if err != nil {
		return nil, err
	}

	caPath := filepath.Join(dir, "control-ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)

	return &harnessControlPKI{
		caCert:     caPath,
		serverCert: serverCert,
		serverKey:  serverKey,
		clientCert: clientCert,
		clientKey:  clientKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS12,
				RootCAs:      pool,
				Certificates: []tls.Certificate{clientPair},
			}},
		},
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
