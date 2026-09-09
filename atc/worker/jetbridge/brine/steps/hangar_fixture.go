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
// The stand-in is github.com/fsouza/fake-gcs-server, reached through the seam
// the production code already has: hangar/gcs.NewStorageClient passes a
// non-empty endpoint to option.WithEndpoint together with
// option.WithoutAuthentication() and storage.WithJSONReads(), which is exactly
// an unauthenticated emulator profile. Nothing in the daemon is modified or
// stubbed to make this work; the daemon's own --durable-endpoint flag is the
// whole seam, and the daemon validates the bucket at boot
// (client.Bucket(...).Attrs), so a fixture pointing at nothing is reported as
// a daemon that exited during startup rather than as a scenario failure later.
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
// TODO(hangar-output, Phase 3 Green): the OUTPUT plane is a separate binary
// against a bucket that is never the durable cache bucket (Req 20).
// hangarOutputDaemonFlags below is the one place that changes.
//
// Half of what that hook was waiting for now exists. Phase 2 added
// cmd/hangar-output-daemon with --output-endpoint, --output-bucket,
// --output-prefix, --output-tenant and its own receipt key, and its publish
// path creates a marked object and signs a receipt. The hook is still not
// flipped, and the reason is precise rather than a matter of taste: the fixture
// starts a daemon and polls it for readiness over HTTP, and the output daemon
// serves no HTTP at all. The plan puts its route table in Phase 3 Green ("Add
// protected versioned output-daemon endpoints ... establish/inspect hold ...
// publish sealed tree") and says of Phase 2 Green "Do not add readiness yet".
//
// So the flip is Phase 3's first act, and it is two lines: return the four
// flags below, and point the readiness poll at the output daemon's own health
// route once it has one. Everything above this comment is unchanged by it.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
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

	"github.com/concourse/concourse/hangar"
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

	// Pending is the canonical archive a step produced and has not published
	// yet. It is bytes on their way to the daemon, not a record of anything the
	// daemon did.
	Pending []byte
}

// hangarOutputDaemonFlags is the single place the output plane's endpoint is
// wired. See the TODO at the head of this file.
func hangarOutputDaemonFlags(endpoint, bucket string) []string {
	// TODO(hangar-output, Phase 3 Green): return
	//   []string{"--output-endpoint", endpoint, "--output-bucket", bucket,
	//            "--output-prefix", …, "--output-tenant", …}
	// and start cmd/hangar-output-daemon instead of cmd/artifact-daemon.
	//
	// The flags exist as of Phase 2; the daemon that would serve this fixture's
	// requests does not, because its route table and readiness are Phase 3
	// Green. Returning them now would put four flags on a binary that would
	// then be polled for a readiness endpoint it does not have, which is a
	// fixture death rather than a scenario failure -- the least useful shape a
	// red can take.
	_, _ = endpoint, bucket
	return nil
}

// startHangarDaemon brings up the emulator (or adopts CI's), creates the output
// bucket, mints the mTLS material every Hangar route requires, and starts the
// real daemon against all of it.
func startHangarDaemon(rec *brine.Recorder) (HangarDaemon, error) {
	ctx := context.Background()

	endpoint, err := hangarEmulatorEndpoint(rec)
	if err != nil {
		return HangarDaemon{}, err
	}

	client, err := hangargcs.NewStorageClient(ctx, endpoint)
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("create the emulator's storage client: %w", err)
	}
	rec.RegisterDisposer(func() { _ = client.Close() })

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
	certDir, err := os.MkdirTemp("", "brine-hangar-tls-*")
	if err != nil {
		return HangarDaemon{}, err
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(certDir) })

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
	scratch, err := os.MkdirTemp("", "brine-hangar-scratch-*")
	if err != nil {
		return HangarDaemon{}, err
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(scratch) })
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("resolve the Hangar scratch directory: %w", err)
	}

	args := append([]string{
		"--hangar-enabled",
		"--hangar-scratch-dir", scratch,
		"--hangar-capability-key", filepath.Join(certDir, "capability.key"),
		"--durable-store", "gcs",
		"--durable-bucket", bucket,
		"--durable-endpoint", endpoint,
		"--tls-cert", filepath.Join(certDir, "server.crt"),
		"--tls-key", filepath.Join(certDir, "server.key"),
		"--tls-ca-cert", filepath.Join(certDir, "ca.crt"),
	}, hangarOutputDaemonFlags(endpoint, bucket)...)

	atcCert, err := tls.X509KeyPair(material.clientCert, material.clientKey)
	if err != nil {
		return HangarDaemon{}, fmt.Errorf("assemble the ATC's client key pair: %w", err)
	}
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{atcCert},
			RootCAs:      material.clientPool,
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
	rec.RegisterDisposer(func() { _ = daemon.stop() })

	return HangarDaemon{
		Daemon:   daemon,
		Ctx:      ctx,
		Endpoint: endpoint,
		Bucket:   bucket,
		Client:   client,
		HTTP:     httpClient,
	}, nil
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
	rec.RegisterDisposer(server.Stop)
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
// scenario starts from, the two store-seeding refinements the dedup scenarios
// need, and the small set of sentences that prove the fixture itself is real.
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

		// The seeding refinements. Neither needs production code that does not
		// exist: an object at a key is an object at a key, and what makes the
		// collision scenarios honest is that the bytes DIFFER from the ones the
		// capture would write.
		brine.DefineMap[HangarDaemon, HangarDaemon](
			"the output bucket already holds {string} whose tree reads {string}",
			func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (HangarDaemon, error) {
				const pattern = "the output bucket already holds {string} whose tree reads {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				body, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				return in, in.seedObject(key, body, map[string]string{
					hangaroutput.MarkerKeyVersion: hangaroutput.MarkerVersion,
				})
			},
		),

		brine.DefineMap[HangarDaemon, HangarDaemon](
			"the output bucket already holds {string} with no marker",
			func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (HangarDaemon, error) {
				key, err := paramAt("the output bucket already holds {string} with no marker", p, 0)
				if err != nil {
					return in, err
				}
				return in, in.seedObject(key, "an object nobody's cohort wrote", nil)
			},
		),

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
