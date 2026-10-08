package steps

// The fixture the whole Hangar output family stands on: a REAL artifact-daemon
// process whose output namespace is a GCS emulator this process controls.
//
// Why an emulator. The output plane's stores are native GCS or the disk store,
// and the daemon enforces it: --execution-control with --output-bucket wants a
// bucket it can validate at boot. The choice is between a GCS stand-in over
// HTTP and a real disk store, and this fixture takes the GCS one.
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
// modified or stubbed to make this work; the daemon's own --output-endpoint
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
// ONE DAEMON, TWO NAMESPACES. The artifact daemon serves its cache from the
// storage root and -- with --execution-control -- the output plane: execution
// control, the capture extension, the input publication route and the managed
// read route, all against the one output bucket, reached over the same mTLS
// channel. Every warrant is verified against the one Hangar key, which is why
// --hangar-key travels with --execution-control. The cache and the output
// namespace are never the same one; a fixture that pointed both at one place
// would be testing a deployment the daemon refuses to be.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
	"google.golang.org/api/option"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// FakeGCSEndpointEnv names the emulator a CI run shares with the Go tier-2
// conformance suite. When it is set no in-process server is started.
const FakeGCSEndpointEnv = "HANGAR_FAKE_GCS_ENDPOINT"

// HangarDaemon is a running artifact daemon whose output namespace is the
// emulated output bucket.
//
// What it does NOT carry is as deliberate as what it does: no request log, no
// handler counters, no list of calls. Every assertion over this state is on
// what the daemon answered or on what the bucket holds afterwards.
type HangarDaemon struct {
	Daemon *realDaemon
	Ctx    context.Context

	// Endpoint is the emulator's base URL. A scenario cannot choose it, nor
	// the bucket: the bucket is created by the fixture and named after nothing
	// the feature file says, which is convention 3 applied to the fixture
	// itself.
	Endpoint string

	// Client reads the bucket back the way the orphan sweep does: through the same
	// unauthenticated emulator profile the daemon uses.
	Client *storage.Client

	// HTTP is a client that trusts the daemon's CA and presents the ATC's
	// client certificate, because every Hangar route is mTLS-protected.
	HTTP *http.Client

	// CertDir holds the one small PKI the daemon was started with. The ATC
	// dials the artifact daemon with the client half of it, so a step that
	// drives production ATC code needs the paths rather than the assembled
	// client.
	CertDir string

	// Output is the daemon that serves the output plane: the same process as
	// Daemon, which mounts it. OutputBucket is the output namespace, the one
	// bucket every publication and managed read in this family goes through.
	Output       *realDaemon
	OutputBucket string

	// Minter is the web's half of the warrant seam: the signer over the one
	// Hangar key the daemon verifies against. A scenario never sees it: the
	// step definitions mint per call, for the purpose and operation the route
	// they are about to call declares.
	Minter *hangar.Signer

	NodeUID string

	// OutputScratch is the output plane's --output-scratch-dir: where the
	// daemon canonicalizes, and where a staged input waits for its publish.
	OutputScratch string

	// Terminations is the standalone daemon's --pod-terminations-dir. There is
	// no kubelet behind this fixture, so a file named after a Pod UID in it is
	// how a scenario says that Pod's containers have all stopped; a capture
	// seal waits for exactly that.
	Terminations string
}

// terminate declares the producing Pod's containers stopped, the way a
// kubelet's status would. A seal of a Pod nobody declared waits and answers
// that it has not terminated yet.
func (s HangarDaemon) terminate(pod executioncontrol.PodUID) error {
	if s.Terminations == "" {
		return fmt.Errorf("this daemon was started with no pod-terminations directory")
	}

	return os.WriteFile(filepath.Join(s.Terminations, string(pod)), nil, 0o600)
}

// stepRoot is the step directory the daemon derives for a capture, under its
// storage root. No request carries it; the fixture knows the shape because it
// plays the producer that writes there.
func (s HangarDaemon) stepRoot(key hangaroutput.CaptureKey) string {
	return filepath.Join(s.Output.Root, "steps", key.Directory())
}

// hangarOutputDaemonFlags is the output plane's whole argv beyond the address,
// state and TLS flags the artifact daemon already has.
//
// --hangar-key goes with --execution-control and nowhere else: the key signs
// every warrant the web presents, and the plane is the only thing on this
// daemon that verifies one. The bucket is the output namespace, which is never
// the cache's.
func hangarOutputDaemonFlags(endpoint, bucket, nodeUID, terminations, keyFile string) []string {
	return []string{
		"--execution-control",
		"--hangar-key", keyFile,
		"--output-endpoint", endpoint,
		"--output-bucket", bucket,
		"--output-prefix", "brine/deployments/one",
		"--output-tenant", "brine-tenant",
		"--node-uid", nodeUID,
		"--pod-terminations-dir", terminations,
	}
}

// The identities the fixture mints. They are constants rather than parameters
// because no scenario may choose one: a Run activation epoch a feature file
// could set would be a feature file choosing which contract admitted it.
const (
	hangarNodeUID = "brine-node-1"
	// runActivationEpoch is the Run contract's activation epoch every Run
	// this fixture admits is born under. It is the Run's, not Hangar's: the
	// output plane has no generation of its own.
	runActivationEpoch = uint64(7)
)

// startHangarDaemon brings up the emulator (or adopts CI's), creates the output
// bucket, mints the mTLS material every Hangar route requires, and starts the
// real daemon against all of it.
//
// The output plane is always served over the daemon's TLS; outputTLS is what
// callers that dialed a once-separate output daemon over TLS asked for, and is
// now always true.
func startHangarDaemon(rec *brine.Recorder, outputTLS ...bool) (HangarDaemon, error) {
	return startHangarDaemonOnNode(rec, hangarNodeUID, true)
}

func startHangarDaemonOnNode(rec *brine.Recorder, nodeUID string, _ bool) (HangarDaemon, error) {
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

	// One small PKI, minted the same way daemon_mtls.go mints its own. Every
	// Hangar route but the node-local ones is behind requireClientCert.
	material, err := mintMTLSMaterial("artifact-daemon", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("mint the daemon's TLS material: %w", err)
	}
	certDir, err := AttributedTempDir("brine-hangar-tls-*")
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar certificate directory", func() error { return os.RemoveAll(certDir) })

	// hangar.key is the one Hangar key: the same bytes every step signs a
	// warrant with (brineHangarKey), which is what makes a warrant this fixture
	// mints one the daemon can verify.
	paths := map[string][]byte{
		"server.crt": material.serverCert,
		"server.key": material.serverKey,
		"ca.crt":     material.caPEM,
		"client.crt": material.clientCert,
		"client.key": material.clientKey,
		"hangar.key": brineHangarKey,
	}
	for name, body := range paths {
		if err := os.WriteFile(filepath.Join(certDir, name), body, 0o600); err != nil {
			return HangarDaemon{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	args := []string{
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

	state := HangarDaemon{
		Ctx:      ctx,
		Endpoint: endpoint,
		Client:   client,
		HTTP:     httpClient,
		CertDir:  certDir,
		NodeUID:  nodeUID,
	}
	outputFlags, err := prepareOutputPlane(rec, &state)
	if err != nil {
		return HangarDaemon{}, err
	}

	// ONE node, ONE storage root, ONE process. The artifact daemon mounts the
	// output plane: the step markers it writes are under the storage root it
	// serves, and its source ledger is what reads them before anything
	// destructive happens.
	daemon, err := startRealDaemonProbed("https", func(url string) error {
		resp, err := httpClient.Get(url + "/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the daemon's output plane is not ready: %d", resp.StatusCode)
		}
		return nil
	}, append(args, outputFlags...)...)
	if err != nil {
		return HangarDaemon{}, err
	}
	TrackDisposer(rec, "the Hangar artifact daemon", daemon.stop)
	state.Daemon = daemon
	state.Output = daemon

	return state, nil
}

// prepareOutputPlane creates the output namespace's bucket and the signer the
// steps mint warrants with, and returns the flags that mount the plane: the
// daemon mounts it with --execution-control and verifies every warrant against
// the Hangar key named beside it.
//
// The bucket is created here and named by the fixture, never by a feature file
// -- convention 3 applied to the fixture itself.
func prepareOutputPlane(rec *brine.Recorder, state *HangarDaemon) ([]string, error) {
	state.OutputBucket = uniqueBucketName()
	if err := createOutputBucket(state.Ctx, state.Endpoint, state.OutputBucket,
		hangarBucketCreateAttempts, hangarBucketCreateTimeout,
		func(attemptCtx context.Context) error {
			return state.Client.Bucket(state.OutputBucket).Create(attemptCtx, "brine-hangar-output", nil)
		}); err != nil {
		return nil, err
	}

	minter, err := hangar.NewSigner(brineHangarKey, time.Minute,
		func() time.Time { return time.Now().UTC() })
	if err != nil {
		return nil, err
	}
	state.Minter = minter

	// The output plane's canonicalization scratch must be absolute and outside
	// the storage root; the daemon checks both.
	scratch, err := AttributedTempDir("brine-hangar-output-scratch-*")
	if err != nil {
		return nil, err
	}
	TrackDisposer(rec, "the output plane's scratch directory", func() error { return os.RemoveAll(scratch) })
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil, fmt.Errorf("resolve the output scratch directory: %w", err)
	}
	state.OutputScratch = scratch

	terminations, err := AttributedTempDir("brine-hangar-terminations-*")
	if err != nil {
		return nil, err
	}
	TrackDisposer(rec, "the output plane's pod-terminations directory",
		func() error { return os.RemoveAll(terminations) })
	state.Terminations = terminations

	flags := hangarOutputDaemonFlags(state.Endpoint, state.OutputBucket, state.NodeUID, terminations,
		filepath.Join(state.CertDir, "hangar.key"))

	return append(flags, "--output-scratch-dir", scratch), nil
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
// features/hangar-publication.feature` did not finish in 300 seconds. What an
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

// HangarFixtureDefinitions is the fixture family: the one Given every Hangar
// scenario starts from.
//
// There is no proving sentence of the fixture's own any more. Every scenario
// in the family exercises the fixture end to end -- the real binary, real
// mTLS, the real GCS client and the emulator -- and if the emulator were
// unreachable, the bucket absent, the endpoint seam broken or the mTLS
// material wrong, the daemon would exit at startup and this Given would say so.
func HangarFixtureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, HangarDaemon](
			"a real artifact daemon publishing to a Hangar output bucket",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (HangarDaemon, error) {
				return startHangarDaemon(rec)
			},
		),

		// The same fixture, named for what the managed-read families need of
		// it: every route but the node-local hold is behind mutual TLS.
		brine.DefineMap[brine.Empty, HangarDaemon](
			"an artifact daemon serving the output plane over authenticated TLS",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (HangarDaemon, error) {
				return startHangarDaemon(rec, true)
			},
		),

		// THE SEEDING REFINEMENTS MOVED, and where they went is the point.
		//
		// They used to take a key -- `the output bucket already holds
		// "hangar/v1/scopes/build/trees/sha256/deadbeef.tar.zst" ...` -- and
		// that key is not one production would ever choose: the scope is an
		// opaque hash of the domain, tenant and store and the digest is over bytes this
		// process did not canonicalize. A scenario seeded at a key like that
		// collides with nothing, and would have gone green against a collision
		// that never happened.
		//
		// So they are now `the output bucket's key for this tree already holds
		// …` in hangar_publication.go, which LEARNS the key by running a probe
		// capture of the same bytes and reading back what the bucket then
		// holds. Convention 3 applied to the fixture: not even the fixture
		// names a location.
	}
}
