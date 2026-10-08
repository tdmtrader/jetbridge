package atctest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/google/uuid"
)

// The node's identities.
const (
	nodeName   = "atctest-node"
	serverName = "artifact-daemon"
)

// node is the one output node: the real artifact daemon, with its output plane
// mounted, over a GCS emulator, and a stand-in for the node's Kubernetes half.
//
// The Kubernetes half is stood in for here, as three answers, each the
// smallest a cluster would give for this one node. These tests are about the
// Run client and the web node, not the adapters that produce the answers:
// jetbridge.OutputDrain and OutputSource.ExecBoundSession run against a real
// API server (envtest) in the Brine features bound-session and
// hangar-recovery, and against a real kubelet in the review-kubelet tier,
// which is also the only place a Pod actually terminates or an exec actually
// carries stdin. The answers:
//   - which node serves the output plane and under what name (node
//     resolution for uploads, result reads, captures and read leases):
//     always this one;
//   - that the producing Pod's containers have all terminated before its step
//     directory is sealed: the harness declares it, through the daemon's
//     standalone --pod-terminations-dir, once its producer has exited;
//   - the kubelet exec that carries a credential stream into the producing
//     Pod's helper (runs.SessionTransport): the bytes are received and the
//     helper's acknowledgement is answered, or refused on demand.
//
// Everything the web node decides about those answers -- authorization,
// claims, readiness, seals, publication -- is production code.
type node struct {
	uid      executioncontrol.NodeUID
	dir      string
	process  *exec.Cmd
	emulator *fakestorage.Server
	client   *jetbridge.OutputControlClient
	http     *http.Client
	// signer holds the one Hangar key the daemon verifies against; it mints
	// the control warrants and the read warrants the harness presents.
	signer        *hangar.Signer
	steps         string
	terminations  string
	endpoint      string
	resultScratch string

	mu        sync.Mutex
	delivered map[int][]byte
	attempts  map[int]int
	// refused reports whether the kubelet exec into a Run's producer fails.
	refused func(runID int) bool
}

func startNode(conn db.DbConn) (n *node, err error) {
	dir, err := os.MkdirTemp("", "atctest-node-")
	if err != nil {
		return nil, err
	}
	// The daemon refuses a scratch or steps root it cannot prove private.
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return nil, err
	}
	// The daemon's storage root holds its control ledger and its steps/; the
	// scratch, TLS and result directories stay outside it.
	n = &node{uid: executioncontrol.NodeUID(uuid.NewString()), dir: dir, steps: filepath.Join(dir, "storage", "steps"),
		delivered: map[int][]byte{}, attempts: map[int]int{}, refused: func(int) bool { return false }}
	defer func() {
		if err != nil {
			n.stop()
		}
	}()
	n.terminations = filepath.Join(dir, "terminations")
	for _, sub := range []string{"storage", filepath.Join("storage", "steps"), "scratch", "tls", "results", "terminations"} {
		if err = os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return n, err
		}
	}
	n.resultScratch = filepath.Join(dir, "results")
	binary, err := buildDaemon()
	if err != nil {
		return n, err
	}
	tlsDir := filepath.Join(dir, "tls")
	pki, err := mintPKI(tlsDir)
	if err != nil {
		return n, err
	}
	n.http = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{pki.client}, RootCAs: pki.roots, ServerName: serverName, MinVersion: tls.VersionTLS12}}}

	key := make([]byte, hangar.WarrantKeyBytes)
	if _, err = rand.Read(key); err != nil {
		return n, err
	}
	if err = os.WriteFile(filepath.Join(tlsDir, "hangar.key"), key, 0o600); err != nil {
		return n, err
	}
	if n.signer, err = hangar.NewSigner(key, time.Minute, time.Now); err != nil {
		return n, err
	}

	bucket := "atctest-output"
	n.emulator, err = fakestorage.NewServerWithOptions(fakestorage.Options{Scheme: "http", Host: "127.0.0.1"})
	if err != nil {
		return n, err
	}
	n.emulator.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})

	n.process = exec.Command(binary,
		"--output-endpoint", n.emulator.URL(), "--output-bucket", bucket, "--output-prefix", "atctest/one", "--output-tenant", "atctest",
		"--pod-terminations-dir", n.terminations,
		"--hangar-key", filepath.Join(tlsDir, "hangar.key"), "--execution-control",
		"--node-uid", string(n.uid),
		"--storage-path", filepath.Join(dir, "storage"), "--output-scratch-dir", filepath.Join(dir, "scratch"),
		// The daemon binds a port of its own choosing and reports it, so no
		// other process can take it between a probe and the bind.
		"--listen-address", "127.0.0.1", "--port", "0",
		"--tls-cert", filepath.Join(tlsDir, "server.crt"), "--tls-key", filepath.Join(tlsDir, "server.key"), "--tls-ca-cert", filepath.Join(tlsDir, "ca.crt"),
	)
	var logs bytes.Buffer
	logged := &lockedWriter{w: &logs}
	n.process.Stdout, n.process.Stderr = logged, logged
	if err = n.process.Start(); err != nil {
		return n, err
	}
	if err = n.awaitReady(logged); err != nil {
		return n, fmt.Errorf("%w\n%s", err, logged.String())
	}
	n.client = jetbridge.NewOutputControlClient(n.endpoint, n.http, n.signer).OnNode(n.uid)
	if err = n.activate(conn); err != nil {
		return n, err
	}
	return n, nil
}

func (n *node) awaitReady(logs *lockedWriter) error {
	const announced = "artifact-daemon listening on "
	deadline := time.Now().Add(60 * time.Second)
	probe := &http.Client{Timeout: 5 * time.Second, Transport: n.http.Transport}
	for time.Now().Before(deadline) {
		if n.endpoint == "" {
			for _, line := range strings.Split(logs.String(), "\n") {
				if addr, ok := strings.CutPrefix(line, announced); ok {
					n.endpoint = "https://" + strings.TrimSpace(addr)
				}
			}
		}
		if n.endpoint != "" {
			response, err := probe.Get(n.endpoint + "/readyz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("the artifact daemon's output plane never became ready")
}

// activate puts the output plane in service: the hangar_enabled row admission
// takes FOR SHARE, as the web's startup write does.
func (n *node) activate(conn db.DbConn) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := db.SetHangarEnabled(ctx, conn, true)
	return err
}

func (n *node) get(ctx context.Context, path string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, n.endpoint+path, nil)
	if err != nil {
		return err
	}
	response, err := n.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", path, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(into)
}

// terminate declares the producing Pod's containers terminated, as the
// kubelet would report them: the seal waits for exactly this.
func (n *node) terminate(pod executioncontrol.PodUID) error {
	return os.WriteFile(filepath.Join(n.terminations, string(pod)), nil, 0o600)
}

// ExecBoundSession stands for the kubelet exec into the producing Pod: the
// helper receives the credential stream and acknowledges readiness for its
// Run, exactly as jb-review-worker auth-handoff prints it. For a team given to
// RefuseDelivery the exec fails after the stream was sent.
func (n *node) ExecBoundSession(_ context.Context, node string, start executioncontrol.Acknowledgement, _ time.Duration, command []string, stdin io.Reader, stdout io.Writer) error {
	if node != nodeName || start.NodeUID != n.uid || start.Kind != executioncontrol.AcknowledgementStart {
		return errors.New("no such producing Pod on this node")
	}
	runID := 0
	for i, arg := range command {
		if arg == "--run-id" && i+1 < len(command) {
			runID, _ = strconv.Atoi(command[i+1])
		}
	}
	if len(command) < 2 || command[1] != "auth-handoff" || runID < 1 {
		return fmt.Errorf("unexpected session command %v", command)
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	refused := n.refused(runID)
	n.mu.Lock()
	n.attempts[runID]++
	if !refused {
		n.delivered[runID] = data
	}
	n.mu.Unlock()
	if refused {
		return errors.New("the exec stream was severed")
	}
	_, err = fmt.Fprintf(stdout, "{\"run_id\":%d,\"status\":\"ready\"}\n", runID)
	return err
}

// hold plays the producing Pod's capture control init: it presents the
// execution, the output and the Pod's UID to the daemon, as the generated
// script does. It is a raw POST because in production it is not a client call.
func (n *node) hold(ctx context.Context, capture output.Capture, pod executioncontrol.PodUID) error {
	warrant, err := n.client.MintGrant(hangar.PurposeControlCapture, "hold", capture.Execution)
	if err != nil {
		return err
	}
	body, err := json.Marshal(output.CaptureHoldRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: capture.Execution,
		Output: capture.Key.Output, PodUID: pod,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(jetbridge.CapabilityHeaderName, string(warrant))
	response, err := n.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		answer, _ := io.ReadAll(response.Body)
		return fmt.Errorf("hold answered %d: %s", response.StatusCode, answer)
	}
	return nil
}

func (n *node) stop() {
	if n.process != nil && n.process.Process != nil {
		_ = n.process.Process.Kill()
		_, _ = n.process.Process.Wait()
	}
	if n.emulator != nil {
		n.emulator.Stop()
	}
	// Materialized inputs are read-only trees; open them before removal.
	_ = filepath.WalkDir(n.dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(n.dir)
}

// transactor adapts the connection to the coordinator's port with the
// production transaction adapter.
type transactor struct{ conn db.DbConn }

func (t transactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := t.conn.Begin()
	if err != nil {
		return nil, err
	}
	return db.HangarOutputTx{Tx: tx}, nil
}

var (
	daemonOnce   sync.Once
	daemonBinary string
	daemonErr    error
)

// buildDaemon builds cmd/artifact-daemon once per test binary.
func buildDaemon() (string, error) {
	daemonOnce.Do(func() {
		root, err := repositoryRoot()
		if err != nil {
			daemonErr = err
			return
		}
		cache, err := os.MkdirTemp("", "atctest-daemon-")
		if err != nil {
			daemonErr = err
			return
		}
		daemonBinary = filepath.Join(cache, "artifact-daemon")
		// Stripped: one link per test process, and the artifact daemon links the
		// cloud SDKs, so an unstripped binary per process is gigabytes of scratch.
		build := exec.Command("go", "build", "-ldflags", "-s -w", "-o", daemonBinary, "./cmd/artifact-daemon")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			daemonErr = fmt.Errorf("building the artifact daemon: %w\n%s", err, out)
		}
	})
	return daemonBinary, daemonErr
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "artifact-daemon")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("atctest: cannot find the repository root")
		}
		dir = parent
	}
}

type pki struct {
	roots          *x509.CertPool
	server, client tls.Certificate
}

// mintPKI writes the node's CA, its server certificate and the web node's
// client certificate. Every daemon route but the hold is mutually
// authenticated, and the daemon verifies the read-lease endpoint by this CA.
func mintPKI(dir string) (pki, error) {
	var out pki
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return out, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "atctest-ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return out, err
	}
	if ca, err = x509.ParseCertificate(caDER); err != nil {
		return out, err
	}
	issue := func(serial int64, name string, usage []x509.ExtKeyUsage) (tls.Certificate, []byte, []byte, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return tls.Certificate{}, nil, nil, err
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			return tls.Certificate{}, nil, nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return tls.Certificate{}, nil, nil, err
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		return pair, certPEM, keyPEM, err
	}
	server, serverCert, serverKey, err := issue(2, serverName, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	if err != nil {
		return out, err
	}
	client, _, _, err := issue(3, "atc", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return out, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	for name, body := range map[string][]byte{"ca.crt": caPEM, "server.crt": serverCert, "server.key": serverKey} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return out, err
		}
	}
	out.roots = x509.NewCertPool()
	out.roots.AppendCertsFromPEM(caPEM)
	out.server, out.client = server, client
	return out, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}
