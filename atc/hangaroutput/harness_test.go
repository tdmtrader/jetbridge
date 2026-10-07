package hangaroutput_test

// The harness these specs run against, and why every part of it is real.
//
// A REAL output daemon, one process per spec, with its own control ledger and
// its own steps root. What a capture does is half a change to a node's
// filesystem that no response shows -- a sealed directory, a released one --
// and a double cannot tell you the answer was right.
//
// A REAL GCS emulator. Requirement 19 admits only the strict native-GCS
// profile, so a filesystem store cannot serve output capture at all; the
// stand-in is the same fake-gcs-server the daemon's own suite uses, reached
// through the daemon's own --output-endpoint flag. Nothing in the daemon is
// stubbed to make it work.
//
// A REAL PostgreSQL, through atc/postgresrunner, with the real repository. The
// whole subject here is which rows exist after a crash, and a fake repository
// would be a second answer to that question.
//
// A REAL HTTP client -- jetbridge's own OutputControlClient, the one production
// uses -- so the capability minting, the facet scoping and the wire encoding
// are the production ones. Importing it here is a test-only edge and creates no
// cycle: the runtime does not import this package, the command that wires them
// together does.
//
// WHAT IS INJECTED, and only this: the ANSWER. A lost commit response, a
// daemon timeout, a lost upload response are all the same shape -- the work
// happened and the caller did not learn it -- so the injectors wrap the real
// collaborator and drop or delay the reply AFTER the real call. Injecting
// before it would be testing a coordinator against a system that did nothing,
// which is a different and much easier problem.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/google/uuid"
	"github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

const (
	harnessNode  = "harness-node-uid"
	harnessEpoch = executioncontrol.ActivationEpoch(7)
	harnessKeyID = "harness-receipt-1"
)

var (
	postmaster    *postgresrunner.Runner
	postmasterRun ifrit.Process
)

// TestMain owns the one postmaster. postgresrunner asserts with gomega in
// non-test code, so a fail handler is registered here -- outside a ginkgo
// suite there is none, and without one gomega panics on the FIRST assertion
// rather than on a failing one.
func TestMain(m *testing.M) {
	gomega.RegisterFailHandler(func(message string, _ ...int) {
		panic("postgresrunner assertion failed outside a suite: " + message)
	})

	var runner postgresrunner.Runner
	postgresrunner.InitializeRunnerForGinkgo(&runner, &postmasterRun)
	postmaster = &runner

	// What was already in the user's temp directory before this package ran.
	// Anything attributable to this process that is still there afterwards is a
	// leak, and it fails the package rather than accumulating silently.
	before := tempSuspects()

	code := m.Run()

	postmasterRun.Signal(os.Interrupt)
	select {
	case <-postmasterRun.Wait():
	case <-time.After(10 * time.Second):
	}

	if leaks := tempLeaks(before); len(leaks) != 0 {
		for _, leak := range leaks {
			fmt.Fprintln(os.Stderr, "temp leak:", leak)
		}
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

// harness is one whole plane: a database, a daemon, a bucket and a coordinator.
type harness struct {
	t *testing.T

	Conn       db.DbConn
	Repository *db.HangarOutputRepository
	Daemon     *daemonProcess
	Store      *fakestorage.Server
	Bucket     string

	Coordinator *hangaroutput.Coordinator
	Recoverer   *hangaroutput.Recoverer
	Drain       *stubDrain
	Dialer      *injectingDialer
}

// announcementKinds reads what a watcher was told, from the DURABLE store.
//
// Not from a recorder in this process: an announcement whose whole reason for
// existing is that the process which decided a disposition may be gone cannot
// be asserted against that process's memory. The store is also the only place
// that can tell "announced" from "announced and then rolled back", which is
// exactly the distinction a terminal announcement written inside its
// disposition's transaction is making.
func (h *harness) announcementKinds(t *testing.T, handoff output.HandoffID) []hangaroutput.AnnouncementKind {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	stored, err := h.Repository.ReadAnnouncements(context.Background(), tx, handoff)
	if err != nil {
		t.Fatalf("reading the announcements: %v", err)
	}

	var kinds []hangaroutput.AnnouncementKind
	for _, announcement := range stored {
		kinds = append(kinds, hangaroutput.AnnouncementKind(announcement.Kind))
	}

	return kinds
}

// recoverUntilSettled drives PRODUCTION's recovery component, not the harness's
// own loop.
//
// The difference is the whole assertion in one spec below: `Recoverer.Run`
// advances every INCOMPLETE handoff by at most one transition, and a handoff
// that has settled is not incomplete and is never visited again. A spec that
// looped `Advance` to quiescence would take a transition production never takes.
func (h *harness) recoverUntilSettled(t *testing.T, handoff output.HandoffID) int {
	t.Helper()

	for pass := 1; pass <= 12; pass++ {
		if err := h.Recoverer.Run(context.Background()); err != nil {
			t.Fatalf("recovery pass %d: %v", pass, err)
		}
		if len(h.incompleteHandoffs(t)) == 0 {
			return pass
		}
	}
	t.Fatalf("twelve recovery passes left %v incomplete", h.incompleteHandoffs(t))

	return 0
}

func (h *harness) incompleteHandoffs(t *testing.T) []output.HandoffID {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	handoffs, err := h.Repository.IncompleteHandoffs(context.Background(), tx,
		hangaroutput.DefaultBatchSize)
	if err != nil {
		t.Fatalf("listing the incomplete handoffs: %v", err)
	}

	return handoffs
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	postmaster.CreateTestDBFromTemplate()
	conn := postmaster.OpenConn()
	t.Cleanup(func() {
		_ = conn.Close()
		postmaster.DropTestDB()
	})

	store, bucket := emulator(t)
	daemon := startDaemon(t, store.URL(), bucket)

	prefix, err := db.HangarConsumerPrefixHeld("hangaroutput-harness")
	if err != nil {
		t.Fatalf("consumer prefix: %v", err)
	}
	repository := db.NewHangarOutputRepository(prefix)

	activate(t, conn, daemon)

	drain := &stubDrain{}
	dialer := &injectingDialer{control: daemon.Client}

	verifier, err := output.NewReceiptSignatureVerifier(daemon.KeyRing, output.ClockFunc(func() time.Time {
		return time.Now().UTC()
	}))
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	transactor := &connTransactor{conn: conn}
	coordinator := &hangaroutput.Coordinator{
		Transactor: transactor,
		Repository: repository,
		Dialer:     dialer,
		Drain:      drain,
		Verifier:   verifier,
		// PRODUCTION's announcer, over the durable store. Requirement 18's
		// announcements outlive the process that made them, and a recorder in
		// this one cannot say whether the store has them -- nor whether the
		// transaction they were written in committed.
		Announcer: hangaroutput.AnnouncerFunc(repository.RecordAnnouncement),
		// The schema types an owner as a uuid: two processes sharing a name
		// would be two owners the fence cannot tell apart.
		OwnerID:      uuid.NewString(),
		ReceiptKeyID: harnessKeyID,
		Now:          func() time.Time { return time.Now().UTC() },
	}

	return &harness{
		t:           t,
		Conn:        conn,
		Repository:  repository,
		Daemon:      daemon,
		Store:       store,
		Bucket:      bucket,
		Drain:       drain,
		Dialer:      dialer,
		Coordinator: coordinator,
		Recoverer: &hangaroutput.Recoverer{
			Coordinator: coordinator,
			Incomplete:  repository,
			Transactor:  transactor,
		},
	}
}

// activate opens the plane. Without an epoch and a fresh, safe attestation
// nothing admits anything, which is the held state the migration leaves behind.
func activate(t *testing.T, conn db.DbConn, daemon *daemonProcess) {
	t.Helper()

	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: daemon.ReceiptPublic})
	_ = public

	if err := postgresrunner.ExecAsActivationRole(conn, `
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES ($1, 'enabled', 'enabled', '{}', '{}', $2,
			now() - interval '1 day', now() + interval '30 days',
			'materialize-key-1', 'gs://harness-output', 'harness/one')`,
		int64(harnessEpoch), harnessKeyID); err != nil {
		t.Fatalf("activating: %v", err)
	}

}

// connTransactor adapts the real connection to the coordinator's port.
type connTransactor struct{ conn db.DbConn }

func (transactor *connTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	// db.HangarOutputTx is the PRODUCTION adapter -- the ATC's own wiring hands
	// out the same one. A harness that unwrapped the commit's typed answer
	// would be specifying a system nobody deploys.
	return db.HangarOutputTx{Tx: tx}, nil
}

// emulator is the strict-native-GCS stand-in, started per spec.
func emulator(t *testing.T) (*fakestorage.Server, string) {
	t.Helper()

	bucket := "harness-output-" + uuid.NewString()[:8]
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme:         "http",
		Host:           "127.0.0.1",
		InitialObjects: []fakestorage.Object{},
	})
	if err != nil {
		t.Fatalf("starting the emulator: %v", err)
	}
	t.Cleanup(server.Stop)
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})

	return server, bucket
}

var (
	daemonBuild    sync.Once
	daemonBinary   string
	daemonBuildErr error
)

func buildDaemon() (string, error) {
	daemonBuild.Do(func() {
		binary := filepath.Join(tempRoot, "artifact-daemon")
		// Stripped: one link per test process, and the artifact daemon links the
		// cloud SDKs, so an unstripped binary per process is gigabytes of scratch.
		build := exec.Command("go", "build", "-ldflags", "-s -w", "-o", binary, "./cmd/artifact-daemon")
		build.Dir = repositoryRoot()
		// The go tool's own work directory goes inside this package's root, so
		// that a build killed by a signal leaves its `go-build*` where this
		// process's cleanup will find it rather than in the user's temp
		// directory forever.
		build.Env = append(os.Environ(), "TMPDIR="+tempRoot)
		if out, err := build.CombinedOutput(); err != nil {
			daemonBuildErr = fmt.Errorf("building the artifact daemon: %w\n%s", err, out)

			return
		}
		daemonBinary = binary
	})

	return daemonBinary, daemonBuildErr
}

func repositoryRoot() string {
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

type daemonProcess struct {
	Endpoint string
	StepsDir string
	Dir      string
	Client   *jetbridge.OutputControlClient
	// Node is a client that trusts the daemon and presents no certificate:
	// what a pod on the node holds.
	Node          *http.Client
	ReceiptPublic []byte
	KeyRing       *output.ReceiptKeyRing

	cmd  *exec.Cmd
	args []string
}

func startDaemon(t *testing.T, endpoint, bucket string) *daemonProcess {
	t.Helper()

	binary, err := buildDaemon()
	if err != nil {
		t.Fatalf("%v", err)
	}

	// t.TempDir, so the spec's own directory goes when the spec goes and a
	// failing run leaves nothing to sweep up by hand.
	dir := t.TempDir()
	for _, sub := range []string{"storage", filepath.Join("storage", "steps"), "scratch"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	receiptKey, receiptPublic := writeEd25519(t, dir, "receipt.pem")
	controlKey, _ := writeEd25519(t, dir, "control.pem")

	secret := make([]byte, executioncontrol.CapabilityKeyBytes)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("secret: %v", err)
	}
	capabilityKey := filepath.Join(dir, "capability.key")
	if err := os.WriteFile(capabilityKey, secret, 0o600); err != nil {
		t.Fatalf("capability key: %v", err)
	}
	// The output read-warrant key, and it is the SAME material the control-plane
	// side of this harness mints warrants with: one key on both sides is what
	// makes a warrant the harness signs one the daemon can verify. It is a THIRD
	// key -- a warrant must not be signable by anything that can mint a
	// publication receipt -- and the daemon refuses a configuration where two
	// of the three are one file.
	materializeKey := filepath.Join(dir, "materialize.key")
	if err := os.WriteFile(materializeKey, readWarrantKey, 0o600); err != nil {
		t.Fatalf("read-warrant key: %v", err)
	}

	port := freePort(t)
	base := fmt.Sprintf("https://127.0.0.1:%d", port)
	// The output plane is TLS-only: one small PKI, a server certificate for
	// 127.0.0.1 and a control-plane client certificate.
	controlClient, nodeClient := writeHarnessPKI(t, dir)

	args := []string{
		"--output-endpoint", endpoint,
		"--output-bucket", bucket,
		"--output-prefix", "harness/one",
		"--output-tenant", "harness",
		"--receipt-key-id", harnessKeyID,
		"--receipt-key-file", receiptKey,
		"--control-key-id", "harness-control-1",
		"--control-key-file", controlKey,
		"--capability-key", capabilityKey,
		"--materialization-key-id", "harness-materialize-1",
		"--materialization-key-file", materializeKey,
		"--node-uid", harnessNode,
		"--activation-epoch", fmt.Sprint(uint64(harnessEpoch)),
		"--storage-path", filepath.Join(dir, "storage"),
		"--output-scratch-dir", filepath.Join(dir, "scratch"),
		"--listen-address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--tls-cert", filepath.Join(dir, "server.crt"),
		"--tls-key", filepath.Join(dir, "server.key"),
		"--tls-ca-cert", filepath.Join(dir, "ca.crt"),
	}

	minter, err := executioncontrol.NewCapabilityMinter(secret, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("minter: %v", err)
	}

	ring, err := output.NewReceiptKeyRing(output.EpochKey{
		KeyID:      harnessKeyID,
		Epoch:      harnessEpoch,
		PublicKey:  ed25519.PublicKey(receiptPublic),
		ValidFrom:  output.NewTimestamp(time.Now().Add(-time.Hour)),
		ValidUntil: output.NewTimestamp(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}

	process := &daemonProcess{
		Endpoint:      base,
		StepsDir:      filepath.Join(dir, "storage", "steps"),
		Dir:           dir,
		ReceiptPublic: receiptPublic,
		KeyRing:       ring,
		Node:          nodeClient,
		args:          args,
	}
	process.Client = jetbridge.NewOutputControlClient(base, controlClient, minter, harnessEpoch)
	process.start(t, binary)

	return process
}

func (process *daemonProcess) start(t *testing.T, binary string) {
	t.Helper()

	daemon := exec.Command(binary, process.args...)
	daemon.Stdout, daemon.Stderr = os.Stderr, os.Stderr
	if err := daemon.Start(); err != nil {
		t.Fatalf("starting the daemon: %v", err)
	}
	process.cmd = daemon
	t.Cleanup(process.Stop)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := process.Node.Get(process.Endpoint + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the daemon never became ready")
}

// Restart stops the process and starts a new one over the SAME control
// directory, which is what makes it a restart rather than a second daemon: the
// ledger it reloads is the one it wrote.
func (process *daemonProcess) Restart(t *testing.T) {
	t.Helper()

	process.Stop()
	binary, err := buildDaemon()
	if err != nil {
		t.Fatalf("%v", err)
	}
	process.start(t, binary)
}

func (process *daemonProcess) Stop() {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	_ = process.cmd.Process.Kill()
	_, _ = process.cmd.Process.Wait()
	process.cmd = nil
}

func writeEd25519(t *testing.T, dir, name string) (string, []byte) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("encoding the key: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}

	return path, public
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// holdSource plays the capture control init container.
//
// It is a raw POST rather than a client method because in production there IS
// no client method: the hold is established by a generated shell script inside
// the producing Pod, presenting a one-shot warrant and the Downward API Pod UID,
// over a route that is exempt from the client certificate for exactly that
// reason. A method on the ATC's client would be a hold the ATC could take, and
// the ATC is not the Pod the hold binds to.
func (process *daemonProcess) holdSource(t *testing.T, admission output.CaptureAdmission,
	incarnation output.SourceIncarnation, pod executioncontrol.PodUID) output.CaptureAcknowledgement {
	t.Helper()

	warrant, err := process.Client.MintGrant(output.CaptureFacet, "hold", admission.Execution)
	if err != nil {
		t.Fatalf("minting the hold warrant: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"protocol_version":    admission.ProtocolVersion,
		"execution":           admission.Execution,
		"activation_epoch":    admission.ActivationEpoch,
		"handoff_id":          admission.HandoffID,
		"source_hold_id":      admission.SourceHoldID,
		"output":              admission.Output,
		"capture_deadline_at": admission.CaptureDeadline,
		"incarnation":         incarnation,
		"pod_uid":             pod,
	})
	if err != nil {
		t.Fatalf("encoding the hold: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost,
		process.Endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the hold request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(jetbridge.CapabilityHeaderName, string(warrant))

	response, err := process.Node.Do(request)
	if err != nil {
		t.Fatalf("holding: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", response.StatusCode, raw)
	}

	var ack output.CaptureAcknowledgement
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("decoding the hold: %v", err)
	}

	return ack
}

// writeHarnessPKI mints a CA, a server certificate for 127.0.0.1 and a client
// certificate under it, writes the server half and the CA into dir, and
// returns a control-plane client (presents the client certificate) and a
// node client (trusts the CA, presents nothing).
func writeHarnessPKI(t *testing.T, dir string) (*http.Client, *http.Client) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "harness-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing the CA: %v", err)
	}
	leaf := func(serial int64, name string, server bool) ([]byte, []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("leaf key: %v", err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if server {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("leaf certificate: %v", err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("leaf key encoding: %v", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCert, serverKey := leaf(2, "artifact-daemon", true)
	clientCert, clientKey := leaf(3, "concourse-client", false)
	for name, body := range map[string][]byte{"ca.crt": caPEM, "server.crt": serverCert, "server.key": serverKey} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	pair, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		t.Fatalf("client key pair: %v", err)
	}
	control := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{pair}}}}
	node := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool}}}

	return control, node
}
