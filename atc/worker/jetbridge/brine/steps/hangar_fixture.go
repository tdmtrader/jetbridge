package steps

// The fixture the whole Hangar output family stands on: a REAL artifact-daemon
// process whose Hangar store is a GCS emulator this process controls.
//
// Why an emulator and not the filesystem store daemon-durable.feature uses.
// Requirement 19 admits only the strict native-GCS profile for Hangar, and the
// daemon enforces it: validateHangarOptions refuses --hangar-enabled unless
// --durable-store=gcs (cmd/artifact-daemon/hangar.go). So the filesystem store
// that made "the bucket" an ordinary directory is not available here, and the
// choice is between a GCS stand-in over HTTP and no real daemon at all.
//
// The stand-in is github.com/fsouza/fake-gcs-server, reached on the same
// endpoint convention the production code uses: a non-empty endpoint goes to
// option.WithEndpoint together with option.WithoutAuthentication() and
// storage.WithJSONReads(), which is exactly an unauthenticated emulator
// profile. The URL shaping is shared through hangar/gcs.NormalizeStorageEndpoint
// -- a string in and a string out -- rather than through a client constructor:
// hangar/gcs hands out no *storage.Client to anything any more, because a
// caller holding one holds an arbitrary object delete. This fixture needs a raw
// client for the one operation no role has permission for (creating a bucket),
// so it builds its own with the SDK and says so. Nothing in the daemon is
// modified or stubbed to make this work; the daemon's own --durable-endpoint
// flag is the whole seam, and the daemon validates the bucket at boot, so a
// fixture pointing at nothing is reported as a daemon that exited during
// startup rather than as a scenario failure later.
//
// THE FIXTURE RECORDS NOTHING (convention 10). fakestorage stores objects with
// their metadata and generations and answers requests; there is no request log
// on this state and no counter a scenario could assert on. "The bucket holds
// exactly one object" is therefore an outcome, not a call count, and seeding a
// DIFFERENT variant at a key is how a scenario tells dedup from overwrite.
// Fault injection, precondition ambiguity and truncation stay in Go against
// hangar/gcs's memoryObjectClient, where the interfaces they need are
// unexported by design.
//
// IN CI the emulator is not started here. HANGAR_FAKE_GCS_ENDPOINT names the
// fake-gcs-server deployment already running in namespace cicd, which is the
// same endpoint the Go tier-2 conformance suite uses, so one deployment serves
// both runners and both share one failure mode.
//
// STARTED IN A GIVEN, not as a ScopeScenario resource, for the reason
// daemon_durable.go records: brine acquires every scenario-scoped resource
// before EVERY scenario in the corpus, and a daemon registered that way cost
// 70 seconds to serve five scenarios.
//
// THE OUTPUT PLANE IS A SECOND DAEMON, and this fixture now starts both.
//
// It has to be two processes and not two roles in one. A Kubernetes service
// account is Pod-wide, so giving the artifact daemon an output-bucket role
// would give its cache and strict-input identity the same role, and Req 20
// forbids the output bucket ever being the durable cache bucket or the
// caller-published strict-input one. The isolation is a second Pod with a
// second service account, which means a second binary -- so a fixture that ran
// one daemon could not express the thing under test.
//
// The artifact daemon here serves the STRICT-INPUT surface, which is what the
// proving sentences at the bottom of this file exercise end to end against the
// emulator. The output daemon serves the capture control API and the publish
// route, against ITS OWN bucket, under its own control and receipt keys.
// hangarOutputDaemonFlags below is where that separation is stated, and a
// fixture that pointed both at one bucket would be testing a deployment the
// daemon refuses to be.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/fsouza/fake-gcs-server/fakestorage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// FakeGCSEndpointEnv names the emulator a CI run shares with the Go tier-2
// conformance suite. When it is set no in-process server is started.
const FakeGCSEndpointEnv = "HANGAR_FAKE_GCS_ENDPOINT"

// HangarDaemon is a running artifact daemon whose Hangar store is the emulated
// output bucket, and the last answer it gave.
//
// What it does NOT carry is as deliberate as what it does: no request log, no
// handler counters, no list of calls. Every assertion over this state is on
// what the daemon answered or on what the bucket holds afterwards.
type HangarDaemon struct {
	Daemon *realDaemon
	Ctx    context.Context

	// Endpoint is the emulator's base URL, and Bucket the bucket this
	// scenario's daemon publishes into. A scenario cannot choose either: the
	// bucket is created by the fixture and named after nothing the feature
	// file says, which is convention 3 applied to the fixture itself.
	Endpoint string
	Bucket   string

	// Client reads the bucket back the way inventory will: through the same
	// unauthenticated emulator profile the daemon uses.
	Client *storage.Client

	// HTTP is a client that trusts the daemon's CA and presents the ATC's
	// client certificate, because every Hangar route is mTLS-protected.
	HTTP *http.Client

	// Published is the last publication answer, decoded as the foundation's
	// attributes. Err carries a transport failure as a value so a refusal is
	// assertable rather than fatal.
	Status    int
	Body      []byte
	Published hangar.TreeAttributes
	Err       error

	// CertDir holds the one small PKI both daemons were started with. The ATC
	// dials the artifact daemon with the client half of it, so a step that
	// drives production ATC code needs the paths rather than the assembled
	// client.
	CertDir string

	// Pending is the canonical archive a step produced and has not published
	// yet. It is bytes on their way to the daemon, not a record of anything the
	// daemon did.
	Pending []byte

	// Output is the second daemon: the output plane's own process, its own
	// bucket, its own keys. OutputBucket is deliberately not Bucket -- Req 20
	// is that they are never the same one, and this state could not express a
	// violation of it if it held one field.
	Output       *realDaemon
	OutputBucket string

	// Minter is the control plane's half of the capability seam. A scenario
	// never sees it: the step definitions mint per call, for the facet and
	// operation the route they are about to call declares.
	Minter *executioncontrol.CapabilityMinter

	// ControlPublic is the activation-pinned public key a scenario verifies a
	// ledger statement under. It is the fixture's, read back from the same file
	// the daemon was given, so an assertion is against production verification
	// rather than against a string a step wrote.
	ControlPublic ed25519.PublicKey
	ReceiptPublic ed25519.PublicKey
}

// hangarOutputDaemonFlags is the output daemon's whole argv beyond the address
// and state flags the launcher supplies.
//
// It names a DIFFERENT bucket from the artifact daemon's, which is the point:
// the two buckets are the trust boundary between the planes.
func hangarOutputDaemonFlags(endpoint, bucket, receiptKey, controlKey, capabilityKey,
	materializeKey string) []string {
	return []string{
		"--output-endpoint", endpoint,
		"--output-bucket", bucket,
		"--output-prefix", "brine/deployments/one",
		"--output-tenant", "brine-tenant",
		"--receipt-key-id", hangarReceiptKeyID,
		"--receipt-key-file", receiptKey,
		"--control-key-id", hangarControlKeyID,
		"--control-key-file", controlKey,
		"--capability-key", capabilityKey,
		"--materialization-key-id", hangarMaterializationKeyID,
		"--materialization-key-file", materializeKey,
		"--node-uid", hangarNodeUID,
		"--activation-epoch", fmt.Sprint(hangarEpoch),
	}
}

// The identities the fixture mints. They are constants rather than parameters
// because no scenario may choose one: an activation epoch a feature file could
// set would be a feature file choosing which key signs its receipts.
const (
	hangarReceiptKeyID = "brine-receipt-key-1"
	// The read-warrant key's id. A THIRD key: a warrant must not be signable by
	// anything that can mint a publication receipt, and the daemon refuses a
	// configuration where two of the three are one file.
	hangarMaterializationKeyID = "brine-materialize-key-1"
	hangarControlKeyID         = "brine-control-key-1"
	hangarNodeUID              = "brine-node-1"
	hangarEpoch                = uint64(7)
)

// startHangarDaemon brings up the emulator (or adopts CI's), creates the output
// bucket, mints the mTLS material every Hangar route requires, and starts the
// real daemon against all of it.
func startHangarDaemon(rec *brine.Recorder) (HangarDaemon, error) {
	ctx := context.Background()

	endpoint, err := hangarEmulatorEndpoint(rec)
	if err != nil {
		return HangarDaemon{}, err
	}

	client, err := emulatorStorageClient(ctx, endpoint)
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the GCS client", client.Close)

	bucket := uniqueBucketName()
	if err := createOutputBucket(ctx, endpoint, bucket,
		hangarBucketCreateAttempts, hangarBucketCreateTimeout,
		func(attemptCtx context.Context) error {
			return client.Bucket(bucket).Create(attemptCtx, "brine-hangar-output", nil)
		}); err != nil {
		return HangarDaemon{}, err
	}

	// One small PKI, minted the same way daemon_mtls.go mints its own. The
	// daemon refuses --hangar-enabled without all three files, and every
	// Hangar route but the materialization one is behind requireClientCert.
	material, err := mintMTLSMaterial("artifact-daemon", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("mint the daemon's TLS material: %w", err)
	}
	certDir, err := AttributedTempDir("brine-hangar-tls-*")
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar certificate directory", func() error { return os.RemoveAll(certDir) })

	paths := map[string][]byte{
		"server.crt":     material.serverCert,
		"server.key":     material.serverKey,
		"ca.crt":         material.caPEM,
		"client.crt":     material.clientCert,
		"client.key":     material.clientKey,
		"capability.key": make([]byte, 32),
	}
	if _, err := rand.Read(paths["capability.key"]); err != nil {
		return HangarDaemon{}, err
	}
	for name, body := range paths {
		if err := os.WriteFile(filepath.Join(certDir, name), body, 0o600); err != nil {
			return HangarDaemon{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	// The scratch directory must be absolute and outside the storage root; the
	// daemon checks both and refuses otherwise.
	scratch, err := AttributedTempDir("brine-hangar-scratch-*")
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar scratch directory", func() error { return os.RemoveAll(scratch) })
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("resolve the Hangar scratch directory: %w", err)
	}

	args := []string{
		"--hangar-enabled",
		"--hangar-scratch-dir", scratch,
		"--hangar-warrant-key", filepath.Join(certDir, "capability.key"),
		"--durable-store", "gcs",
		"--durable-bucket", bucket,
		"--durable-endpoint", endpoint,
		"--tls-cert", filepath.Join(certDir, "server.crt"),
		"--tls-key", filepath.Join(certDir, "server.key"),
		"--tls-ca-cert", filepath.Join(certDir, "ca.crt"),
	}

	atcCert, err := tls.X509KeyPair(material.clientCert, material.clientKey)
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("assemble the ATC's client key pair: %w", err)
	}
	// Verify the daemon against the CA that signed it, the way the ATC does
	// from the PEM on disk, rather than against a pool the fixture kept.
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(material.caPEM) {
		return HangarDaemon{}, fmt.Errorf("the fixture CA certificate is not parseable PEM")
	}
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{atcCert},
			RootCAs:      rootCAs,
			ServerName:   "artifact-daemon",
		}},
	}

	daemon, err := startRealDaemonProbed("https", func(url string) error {
		resp, err := httpClient.Get(url + "/healthz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}, args...)
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar artifact daemon", daemon.stop)

	state := HangarDaemon{
		Daemon:   daemon,
		Ctx:      ctx,
		Endpoint: endpoint,
		Bucket:   bucket,
		Client:   client,
		HTTP:     httpClient,
		CertDir:  certDir,
	}

	return startOutputDaemon(rec, state, certDir)
}

// startOutputDaemon brings up the second binary: its own bucket, its own two
// Ed25519 keys, its own capability secret.
//
// The bucket is created here and named by the fixture, never by a feature file
// -- convention 3 applied to the fixture itself -- and it is a different bucket
// from the artifact daemon's, which is the whole reason there are two daemons.
func startOutputDaemon(rec *brine.Recorder, state HangarDaemon, certDir string) (HangarDaemon, error) {
	state.OutputBucket = uniqueBucketName()
	if err := createOutputBucket(state.Ctx, state.Endpoint, state.OutputBucket,
		hangarBucketCreateAttempts, hangarBucketCreateTimeout,
		func(attemptCtx context.Context) error {
			return state.Client.Bucket(state.OutputBucket).Create(attemptCtx, "brine-hangar-output", nil)
		}); err != nil {
		return HangarDaemon{}, err
	}

	receiptKey, receiptPublic, err := writeEd25519Key(certDir, "receipt.pem")
	if err != nil {
		return HangarDaemon{}, err
	}
	controlKey, controlPublic, err := writeEd25519Key(certDir, "control.pem")
	if err != nil {
		return HangarDaemon{}, err
	}
	state.ReceiptPublic, state.ControlPublic = receiptPublic, controlPublic

	capabilitySecret := make([]byte, executioncontrol.CapabilityKeyBytes)
	if _, err := rand.Read(capabilitySecret); err != nil {
		return HangarDaemon{}, err
	}
	capabilityFile := filepath.Join(certDir, "capability-control.key")
	if err := os.WriteFile(capabilityFile, capabilitySecret, 0o600); err != nil {
		return HangarDaemon{}, err
	}
	// The output read-warrant key, which is the SAME material the consumer-side
	// fixture mints warrants with (brineReadWarrantKey): one key on both sides is
	// what makes a warrant this fixture signs one the daemon can verify.
	materializeFile := filepath.Join(certDir, "materialize.key")
	if err := os.WriteFile(materializeFile, brineReadWarrantKey, 0o600); err != nil {
		return HangarDaemon{}, err
	}

	minter, err := executioncontrol.NewCapabilityMinter(capabilitySecret, time.Minute,
		func() time.Time { return time.Now().UTC() })
	if err != nil {
		return HangarDaemon{}, err
	}
	state.Minter = minter

	// ONE node, ONE storage root. The output daemon is started in the artifact
	// daemon's root, which is what a real node looks like: the control
	// directory the output daemon writes is under the hostPath the artifact
	// daemon serves, and the artifact daemon's read-only classifier is what
	// reads it before anything destructive happens. Two roots made the
	// classifier answer "unmanaged" about every held source, because it was
	// reading a directory the other daemon never wrote to.
	output, err := startNamedDaemonInRoot("hangar-output-daemon", state.Daemon.Root, "http",
		func(url string) error {
			resp, err := http.Get(url + "/readyz")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("the output daemon is not ready: %d", resp.StatusCode)
			}

			return nil
		}, hangarOutputDaemonFlags(state.Endpoint, state.OutputBucket,
			receiptKey, controlKey, capabilityFile, materializeFile)...)
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar output daemon", output.stop)
	state.Output = output

	return state, nil
}

// writeEd25519Key mints one key pair and writes the private half where the
// daemon expects it, returning the public half so a scenario can verify against
// the same key the daemon signed with.
func writeEd25519Key(dir, name string) (string, ed25519.PublicKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return "", nil, err
	}

	return path, public, nil
}

// hangarEmulatorEndpoint adopts CI's shared fake-gcs-server when one is named,
// and otherwise starts one in this process — there is no Docker daemon on the
// development Mac, so the library, not the container, is the local form.
func hangarEmulatorEndpoint(rec *brine.Recorder) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(FakeGCSEndpointEnv)); configured != "" {
		return configured, nil
	}
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme:     "http",
		Host:       "127.0.0.1",
		Port:       0,
		NoListener: false,
	})
	if err != nil {
		return "", fmt.Errorf("start the in-process GCS emulator: %w", err)
	}
	TrackDisposer(rec, "the in-process GCS emulator", func() error { server.Stop(); return nil })
	return server.URL(), nil
}

// The bucket create is bounded, and it is bounded because it was not.
//
// The GCS client retries a refused connection with backoff and the fixture
// handed it a background context, so an endpoint nothing answers -- a service
// name mistyped in the pipeline, say -- never returned. Measured: with
// HANGAR_FAKE_GCS_ENDPOINT pointed at a closed port, `brine run
// features/hangar-fixture.feature` did not finish in 300 seconds. What an
// operator got was the brine job's 30-minute timeout with no reason in it,
// which is the least useful shape a failure can take.
const (
	hangarBucketCreateAttempts = 3
	hangarBucketCreateTimeout  = 10 * time.Second
)

// createOutputBucket attempts the create a bounded number of times, each under
// its own deadline, and fails with the endpoint in the message.
//
// The attempts and the per-attempt bound are parameters rather than the
// constants above so the closed-port case can be asserted in a second rather
// than in half a minute; the production call site passes the constants.
// emulatorStorageClient is the fixture's own raw client, for the one operation
// the production seam deliberately cannot do: creating a bucket.
//
// It is built with the SDK rather than obtained from hangar/gcs because that
// package hands out no *storage.Client to anybody -- a caller holding one holds
// an arbitrary, unconditional object delete, which is how one finding reached
// three rounds. The endpoint convention is still shared, through a function
// that takes a string and returns a string.
func emulatorStorageClient(ctx context.Context, endpoint string) (*storage.Client, error) {
	normalized, err := hangargcs.NormalizeStorageEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("normalise the emulator endpoint %q: %w", endpoint, err)
	}
	client, err := storage.NewClient(ctx, option.WithEndpoint(normalized),
		option.WithoutAuthentication(), storage.WithJSONReads())
	if err != nil {
		return nil, fmt.Errorf("create the emulator's storage client: %w", err)
	}

	return client, nil
}

func createOutputBucket(ctx context.Context, endpoint, bucket string, attempts int, perAttempt time.Duration, create func(context.Context) error) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttempt)
		err = create(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("create the Hangar output bucket %q on %s: gave up after %d attempts of "+
		"at most %s each: %w.\n\nIf %s names a Kubernetes service, check that it resolves from "+
		"this pod. Waiting instead would be this job's timeout with no reason in it.",
		bucket, endpoint, attempts, perAttempt, err, FakeGCSEndpointEnv)
}

func uniqueBucketName() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		// A bucket name that collides is a scenario that fails loudly at
		// creation, which is better than a fixture that cannot start at all.
		return "hangar-output-fallback"
	}
	return fmt.Sprintf("hangar-output-%x", raw)
}

// objectKeys lists what the output bucket holds, in the order the API returns
// them. This is the only read of the store a scenario gets, and it is a read of
// the bucket rather than of anything the fixture remembered.
func (s HangarDaemon) objectKeys() ([]string, error) {
	var keys []string
	it := s.Client.Bucket(s.Bucket).Objects(s.Ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list the Hangar output bucket %q: %w", s.Bucket, err)
		}
		keys = append(keys, attrs.Name)
	}
	return keys, nil
}

func (s HangarDaemon) objectMarkerVersion(key string) (string, error) {
	attrs, err := s.Client.Bucket(s.Bucket).Object(key).Attrs(s.Ctx)
	if err != nil {
		return "", fmt.Errorf("stat %q in the Hangar output bucket: %w", key, err)
	}
	return attrs.Metadata[hangaroutput.MarkerKeyVersion], nil
}

// publish sends a raw tar to the strict publication route. The daemon
// canonicalizes it, stores it in the emulated bucket and answers with the
// foundation's attributes — scope, digest, generation, sizes.
func (s HangarDaemon) publish(scope string, archive []byte) HangarDaemon {
	resp, err := s.HTTP.Post(
		s.Daemon.URL+"/hangar/v1/scopes/"+scope+"/trees",
		"application/octet-stream",
		strings.NewReader(string(archive)),
	)
	if err != nil {
		s.Status, s.Body, s.Err = 0, nil, err
		return s
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	s.Status, s.Body, s.Err = resp.StatusCode, body, readErr
	if resp.StatusCode/100 == 2 && readErr == nil {
		s.Err = json.Unmarshal(body, &s.Published)
	}
	return s
}

// seedObject writes an object straight into the output bucket, the way an
// object from some other node on some other day got there. It is the
// daemon-durable seeding device, ported: publishing twice cannot tell dedup
// from overwrite, so the discriminator has to be a DIFFERENT variant already
// sitting at the key.
func (s HangarDaemon) seedObject(key, body string, metadata map[string]string) error {
	writer := s.Client.Bucket(s.Bucket).Object(key).NewWriter(s.Ctx)
	writer.Metadata = metadata
	if _, err := io.WriteString(writer, body); err != nil {
		_ = writer.Close()
		return fmt.Errorf("seed %q in the Hangar output bucket: %w", key, err)
	}
	return writer.Close()
}

// HangarFixtureDefinitions is the fixture family: the one Given every Hangar
// scenario starts from, and the small set of sentences that prove the fixture
// itself is real.
//
// The proving sentences are strict-input ones on purpose. They exercise the
// only Hangar surface the daemon has today — canonicalize a tar, store it under
// a server-derived key, answer with the foundation's attributes — end to end
// through the real binary, real mTLS, the real GCS client and the emulator. If
// the emulator were unreachable, the bucket absent, the endpoint seam broken or
// the mTLS material wrong, the daemon would exit at startup and this Given would
// say so.
func HangarFixtureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, HangarDaemon](
			"a real artifact daemon publishing to a Hangar output bucket",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (HangarDaemon, error) {
				return startHangarDaemon(rec)
			},
		),

		// THE SEEDING REFINEMENTS MOVED, and where they went is the point.
		//
		// They used to take a key -- `the output bucket already holds
		// "hangar/v1/scopes/build/trees/sha256/deadbeef.tar.zst" ...` -- and
		// that key is not one production would ever choose: the scope is an
		// opaque per-tenant, per-epoch hash and the digest is over bytes this
		// process did not canonicalize. A scenario seeded at a key like that
		// collides with nothing, and would have gone green against a collision
		// that never happened.
		//
		// So they are now `the output bucket's key for this tree already holds
		// …` in hangar_publication.go, which LEARNS the key by running a probe
		// capture of the same bytes and reading back what the bucket then
		// holds. Convention 3 applied to the fixture: not even the fixture
		// names a location.

		// The proving sentences.
		brine.DefineMap[HangarDaemon, HangarDaemon](
			"a step produced a tree whose file {string} reads {string}",
			func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (HangarDaemon, error) {
				const pattern = "a step produced a tree whose file {string} reads {string}"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				archive, err := durableTarOfOneFile(name, content)
				if err != nil {
					return in, err
				}
				in.Pending = archive
				return in, nil
			},
		),

		brine.DefineMap[HangarDaemon, HangarDaemon](
			"the tree is published to the scope {string}",
			func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (HangarDaemon, error) {
				scope, err := paramAt("the tree is published to the scope {string}", p, 0)
				if err != nil {
					return in, err
				}
				if len(in.Pending) == 0 {
					return in, fmt.Errorf("no tree was produced to publish")
				}
				return in.publish(scope, in.Pending), nil
			},
		),

		CheckInt[HangarDaemon]("the Hangar daemon answers {int}",
			"the daemon's status",
			func(in HangarDaemon) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Status, nil
			},
			func(in HangarDaemon) string { return "body: " + abbrev(string(in.Body)) }),

		CheckString[HangarDaemon]("the published tree is named by the scope {string}",
			"the scope the daemon derived",
			func(in HangarDaemon) (string, error) {
				if in.Err != nil {
					return "", in.Err
				}
				return string(in.Published.Ref.Scope), nil
			}),

		// The generation is what proves the object reached a STORE rather than a
		// buffer: it is assigned by the bucket at creation and is not knowable
		// to the daemon before the write lands.
		CheckThat[HangarDaemon]("the published tree carries a store-assigned generation",
			func(in HangarDaemon) error {
				if in.Err != nil {
					return in.Err
				}
				if err := in.Published.Ref.Validate(); err != nil {
					return fmt.Errorf("the daemon's answer is not a valid tree reference: %w", err)
				}
				if in.Published.Ref.Generation <= 0 {
					return fmt.Errorf("expected a positive generation, got %d", in.Published.Ref.Generation)
				}
				return nil
			}),

		CheckCount[HangarDaemon]("the Hangar output bucket holds exactly {int} objects",
			"objects in the output bucket",
			func(in HangarDaemon) ([]string, error) { return in.objectKeys() }),
	}
}
