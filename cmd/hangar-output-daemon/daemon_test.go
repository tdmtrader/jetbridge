package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// The daemon's construction and its publish path.
//
// stdlib testing, matching cmd/artifact-daemon (0 Ginkgo / 45 stdlib), and a
// real emulator rather than a stub store: what is under test is a binary that
// talks to a bucket, and a construction test against a double would prove the
// double was constructible.

func writeReceiptKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key pair: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("encoding the private key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "receipt.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing the private key: %v", err)
	}

	return path, public
}

func emulator(t *testing.T) (*fakestorage.Server, string) {
	t.Helper()

	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme: "http", Host: "127.0.0.1", Port: 0,
	})
	if err != nil {
		t.Fatalf("starting fake-gcs-server: %v", err)
	}
	t.Cleanup(server.Stop)

	bucket := "output-plane-test"
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})

	return server, bucket
}

func validConfig(t *testing.T, endpoint, bucket string) Config {
	t.Helper()

	keyFile, _ := writeReceiptKey(t)

	return Config{
		OutputStore:       output.StoreGCS,
		OutputEndpoint:    endpoint,
		OutputBucket:      bucket,
		OutputPrefix:      "deployments/blue",
		OutputTenant:      "tenant-a",
		CacheBucket:       "deployment-durable-cache",
		StrictInputBucket: "deployment-strict-input",
		ReceiptKeyID:      "receipt-key-1",
		ReceiptKeyFile:    keyFile,
		ActivationEpoch:   7,
		OperationTimeout:  10 * time.Second,
	}
}

func TestTheDaemonRefusesToBePointedAtAnotherPlanesBucket(t *testing.T) {
	server, bucket := emulator(t)

	// The control, first: the valid configuration builds.
	if _, err := Build(context.Background(), validConfig(t, server.URL(), bucket)); err != nil {
		t.Fatalf("the valid configuration did not build: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"the durable cache bucket":                   func(c *Config) { c.OutputBucket = c.CacheBucket },
		"the strict-input bucket":                    func(c *Config) { c.OutputBucket = c.StrictInputBucket },
		"a shared bucket with prefix-only isolation": func(c *Config) { c.SharedBucketPrefixOnlyIsolation = true },
		"a filesystem store":                         func(c *Config) { c.OutputStore = "filesystem" },
		"an S3-compatible store":                     func(c *Config) { c.OutputStore = "s3" },
		"no bucket":                                  func(c *Config) { c.OutputBucket = "" },
		"no tenant":                                  func(c *Config) { c.OutputTenant = "" },
		"no epoch":                                   func(c *Config) { c.ActivationEpoch = 0 },
		"no receipt key id":                          func(c *Config) { c.ReceiptKeyID = "" },
		"no receipt key file":                        func(c *Config) { c.ReceiptKeyFile = "" },
		"a non-positive timeout":                     func(c *Config) { c.OperationTimeout = 0 },
	} {
		config := validConfig(t, server.URL(), bucket)
		mutate(&config)

		if _, err := Build(context.Background(), config); err == nil {
			t.Errorf("the daemon was built with %s", name)
		}
	}
}

func TestTheDaemonSignsReceiptsWithAnEd25519KeyAndNothingElse(t *testing.T) {
	server, bucket := emulator(t)

	// An RSA key is the interesting refusal: it parses as a PKCS#8 private key
	// and would sign perfectly well, producing receipts no verifier in this
	// cohort can check.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating an RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("encoding the RSA key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "rsa.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing the RSA key: %v", err)
	}

	config := validConfig(t, server.URL(), bucket)
	config.ReceiptKeyFile = path
	if _, err := Build(context.Background(), config); !errors.Is(err, output.ErrUnsupportedProtocol) {
		t.Errorf("the daemon accepted an RSA receipt key: %v", err)
	}

	// And a file that is not PEM at all.
	notPEM := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(notPEM, []byte("this is not a key"), 0o600); err != nil {
		t.Fatalf("writing the garbage key: %v", err)
	}
	config.ReceiptKeyFile = notPEM
	if _, err := Build(context.Background(), config); !errors.Is(err, output.ErrCorrupt) {
		t.Errorf("the daemon accepted a key file that is not PEM: %v", err)
	}
}

func TestThePublishPathCreatesAnObjectAndSignsAVerifiableReceipt(t *testing.T) {
	server, bucket := emulator(t)
	config := validConfig(t, server.URL(), bucket)

	daemon, err := Build(context.Background(), config)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	namespace := daemon.Namespace()
	digest := hangarDigest("ab")
	reservation := output.ResolvedReservation{
		ReservationID:   "44444444-4444-4444-8444-444444444444",
		Execution:       executioncontrol.Identity{ExecutionID: "33333333-3333-4333-8333-333333333333", Fence: 1},
		ActivationEpoch: 7,
		HandoffID:       "11111111-1111-4111-8111-111111111111",
		CaptureFence:    5,
		Scope:           namespace.Scope(),
		Digest:          digest,
		Marker: namespace.MarkerFor("44444444-4444-4444-8444-444444444444", digest,
			output.NewTimestamp(time.Now().UTC())),
	}

	request := PublishRequest{Reservation: reservation}

	body := []byte("a sealed canonical tree")
	object, err := daemon.Publish(context.Background(), request,
		bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}

	if object.Deduplicated {
		t.Error("the first publish reported deduplication")
	}
	if object.Attributes.Ref.Generation <= 0 {
		t.Error("the published object has no generation")
	}

	// The challenge exists only now: it names the generation the publish
	// assigned, which is why the receipt cannot be signed one call earlier.
	issuedAt := time.Now().UTC()
	challenge := output.StatChallenge{
		Nonce:           "nonce-0123456789abcdef",
		HandoffID:       reservation.HandoffID,
		ReservationID:   reservation.ReservationID,
		ActivationEpoch: namespace.ActivationEpoch(),
		Ref:             object.Attributes.Ref,
		CaptureFence:    reservation.CaptureFence,
		IssuedAt:        output.NewTimestamp(issuedAt),
		NotAfter:        output.NewTimestamp(issuedAt.Add(5 * time.Minute)),
	}

	receipt, attested, err := daemon.StatExact(context.Background(), challenge, output.ReceiptClaims{
		Execution:            reservation.Execution,
		ProducerCheckpointID: "opaque-checkpoint",
		Incarnation: output.SourceIncarnation{
			ExecutionID:      "33333333-3333-4333-8333-333333333333",
			NodeUID:          "node-1",
			HandleGeneration: 3,
			Output:           "result",
		},
		Output:      "result",
		WriterFence: 9,
	})
	if err != nil {
		t.Fatalf("attesting: %v", err)
	}

	if attested.Attributes.Ref != object.Attributes.Ref {
		t.Errorf("the attesting stat observed %v and the publish reported %v",
			attested.Attributes.Ref, object.Attributes.Ref)
	}
	if receipt.Claims.Ref != object.Attributes.Ref {
		t.Errorf("the receipt names %v and the object is %v", receipt.Claims.Ref, object.Attributes.Ref)
	}
	if receipt.Claims.ActivationEpoch != namespace.ActivationEpoch() {
		t.Errorf("the receipt claims epoch %d", receipt.Claims.ActivationEpoch)
	}
	if receipt.Claims.ChallengeNonce != challenge.Nonce {
		t.Errorf("the receipt answers challenge %q and the daemon was handed %q",
			receipt.Claims.ChallengeNonce, challenge.Nonce)
	}
	if !receipt.Claims.ChallengeIssuedAt.Equal(challenge.IssuedAt.Time) {
		t.Errorf("the receipt says its challenge was issued at %s and it was issued at %s",
			receipt.Claims.ChallengeIssuedAt.UTC(), challenge.IssuedAt.UTC())
	}

	// Verified with the production verifier under the activation-pinned public
	// key, never against a string this test wrote.
	ring, err := output.NewReceiptKeyRing(output.EpochKey{
		KeyID:      config.ReceiptKeyID,
		Epoch:      namespace.ActivationEpoch(),
		PublicKey:  daemon.ReceiptPublicKey(),
		ValidFrom:  output.NewTimestamp(time.Now().UTC().Add(-time.Hour)),
		ValidUntil: output.NewTimestamp(time.Now().UTC().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("pinning the key ring: %v", err)
	}
	verifier, err := output.NewReceiptSignatureVerifier(ring, output.ClockFunc(nowUTC))
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	if err := verifier.Verify(receipt, challenge); err != nil {
		t.Errorf("the daemon's own receipt does not verify under the pinned key: %v", err)
	}

	// The bucket holds exactly one object, under the derived prefix and scope.
	keys := listKeys(t, server, bucket)
	if len(keys) != 1 {
		t.Fatalf("the bucket holds %d objects: %v", len(keys), keys)
	}
	expected, err := namespace.ObjectKey(digest)
	if err != nil {
		t.Fatalf("deriving the key: %v", err)
	}
	if keys[0] != expected {
		t.Errorf("the object landed at %q, the derived key is %q", keys[0], expected)
	}
}

func TestACallerChosenNamespaceIsRefusedByThePublishPath(t *testing.T) {
	server, bucket := emulator(t)
	daemon, err := Build(context.Background(), validConfig(t, server.URL(), bucket))
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	for field, chosen := range map[string]output.CallerNamespaceRequest{
		"bucket": {Bucket: "somebody-elses-bucket"},
		"scope":  {Scope: "o0000000000000000000000000000000000000000"},
		"key":    {Key: "hangar/v1/scopes/x/trees/sha256/dead.tar.zst"},
		"prefix": {Prefix: "deployments/red"},
	} {
		_, err := daemon.Publish(context.Background(),
			PublishRequest{Namespace: chosen},
			bytes.NewReader([]byte("body")), 4)
		if !errors.Is(err, output.ErrUnauthorized) {
			t.Errorf("a publish naming a caller-chosen %s was answered with %v, expected "+
				"ErrUnauthorized", field, err)
		}
		if err != nil && !strings.Contains(err.Error(), field) {
			t.Errorf("the refusal for a caller-chosen %s does not name the field: %v", field, err)
		}
	}

	// The bucket is untouched: a refused request publishes nothing.
	if keys := listKeys(t, server, bucket); len(keys) != 0 {
		t.Errorf("a refused publish left %v in the bucket", keys)
	}
}

func TestTheDaemonBindsEveryFlagItNeeds(t *testing.T) {
	// A flag that exists in the chart and not in the binary, or the other way
	// round, is how a deployment silently keeps its default. The chart's own
	// drift test is Phase 8's; this is the half that says the binary has them.
	config := Config{}
	flags := flag.NewFlagSet("hangar-output-daemon", flag.ContinueOnError)
	BindFlags(flags, &config)

	for _, name := range []string{
		"output-store", "output-endpoint", "output-bucket", "output-prefix", "output-tenant",
		"cache-bucket", "strict-input-bucket", "shared-bucket-prefix-only-isolation",
		"receipt-key-id", "receipt-key-file", "activation-epoch", "output-timeout",
	} {
		if flags.Lookup(name) == nil {
			t.Errorf("the daemon has no --%s flag", name)
		}
	}

	// And the flags it must NOT have. A durable-store flag here would be the
	// bucket isolation undone by a helm value.
	for _, forbidden := range []string{
		"durable-store", "durable-bucket", "durable-endpoint", "durable-path",
		"storage-path", "hangar-enabled", "hangar-capability-key",
	} {
		if flags.Lookup(forbidden) != nil {
			t.Errorf("the output daemon declares --%s. It has no cache client and no "+
				"strict-input client; a flag that lets an operator give it one is the isolation "+
				"undone by configuration", forbidden)
		}
	}

	if err := flags.Parse([]string{"--output-bucket", "b", "--output-tenant", "t"}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if config.OutputBucket != "b" || config.OutputTenant != "t" {
		t.Errorf("the flags did not bind: %+v", config)
	}
	if config.OutputStore != output.StoreGCS {
		t.Errorf("the default store is %q, expected %q", config.OutputStore, output.StoreGCS)
	}
}

func TestTheDaemonNeverProbesTheBucketAtStartup(t *testing.T) {
	// The foundation's publisher calls Bucket.Attrs to fail fast. Copying it
	// here would need storage.buckets.get on the publisher principal, which is
	// a bucket-policy permission the output publisher must not hold; the
	// attestor owns bucket verification. So a bucket that does not exist must
	// still build, and fail on the first publish instead.
	server, _ := emulator(t)
	config := validConfig(t, server.URL(), "a-bucket-that-was-never-created")

	if _, err := Build(context.Background(), config); err != nil {
		t.Fatalf("the daemon probed the bucket at startup: %v", err)
	}
}

func TestTheDaemonHoldsOnlyThePublisherRole(t *testing.T) {
	// The type-level half of the principal boundary, from this binary's side.
	// The import-side half is the repository's own
	// TestTheOutputRolesAreLinkedOnlyByTheirOwnPrincipals, and the role
	// interfaces' own method sets are asserted in hangar/output/conformance;
	// what is checked here is that THIS daemon's field is that narrow role and
	// not a full client that happens to be used narrowly.
	handle := reflect.TypeOf((*publisher.Handle)(nil)).Elem()
	for index := 0; index < handle.NumMethod(); index++ {
		switch name := handle.Method(index).Name; name {
		case "Delete", "List":
			t.Errorf("the publisher handle this daemon holds offers %s. Its Pod's service "+
				"account is the output publisher; a method here is a permission there", name)
		}
	}

	daemonType := reflect.TypeOf(Daemon{})
	field, ok := daemonType.FieldByName("publisher")
	if !ok {
		t.Fatal("the Daemon has no publisher field; this rule is guarding nothing")
	}
	if field.Type != reflect.TypeOf((*publisher.Publisher)(nil)) {
		t.Errorf("the Daemon's publisher field is %v, not *publisher.Publisher. A wider type "+
			"here is a wider capability, whatever this process happens to call today", field.Type)
	}
	if _, ok := daemonType.FieldByName("store"); ok {
		t.Error("the Daemon holds an object store directly, bypassing the role restriction")
	}
}

func listKeys(t *testing.T, server *fakestorage.Server, bucket string) []string {
	t.Helper()

	objects, _, err := server.ListObjects(bucket, "", "", false)
	if err != nil {
		t.Fatalf("listing %s: %v", bucket, err)
	}

	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Name)
	}

	return keys
}

func hangarDigest(fill string) hangar.Digest {
	return hangar.Digest("sha256:" + strings.Repeat(fill, 32))
}

// StatExact's own refusals (Phase 2 checkpoint review round 2, R2-1).
//
// Each of these is defence in depth: the receipt copies ReservationID and
// ActivationEpoch out of the challenge, Verify.bind compares them, and
// hangar_receipt_admission refuses a challenge issued for another capture at
// registration time. But they are three branches of new production code, and
// before this test each of them could be deleted with the suite staying green.
//
// The control is the happy path in
// TestThePublishPathCreatesAnObjectAndSignsAVerifiableReceipt above; here each
// row starts from a real published object and changes exactly one fact.
func TestTheAttestingStatRefusesAChallengeItCannotAnswer(t *testing.T) {
	server, bucket := emulator(t)
	config := validConfig(t, server.URL(), bucket)

	daemon, err := Build(context.Background(), config)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	namespace := daemon.Namespace()
	const mine = output.ReservationID("44444444-4444-4444-8444-444444444444")
	const somebodyElse = output.ReservationID("55555555-5555-4555-8555-555555555555")
	digest := hangarDigest("ab")

	body := []byte("a sealed canonical tree")
	object, err := daemon.Publish(context.Background(), PublishRequest{
		Reservation: output.ResolvedReservation{
			ReservationID:   mine,
			Execution:       executioncontrol.Identity{ExecutionID: "33333333-3333-4333-8333-333333333333", Fence: 1},
			ActivationEpoch: 7,
			HandoffID:       "11111111-1111-4111-8111-111111111111",
			CaptureFence:    5,
			Scope:           namespace.Scope(),
			Digest:          digest,
			Marker: namespace.MarkerFor(mine, digest,
				output.NewTimestamp(time.Now().UTC())),
		},
	}, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}

	// The control: the challenge this object really answers is answered.
	valid := func() output.StatChallenge {
		issuedAt := time.Now().UTC()

		return output.StatChallenge{
			Nonce:           "nonce-0123456789abcdef",
			HandoffID:       "11111111-1111-4111-8111-111111111111",
			ReservationID:   mine,
			ActivationEpoch: namespace.ActivationEpoch(),
			Ref:             object.Attributes.Ref,
			CaptureFence:    5,
			IssuedAt:        output.NewTimestamp(issuedAt),
			NotAfter:        output.NewTimestamp(issuedAt.Add(5 * time.Minute)),
		}
	}
	claims := output.ReceiptClaims{
		Execution:            executioncontrol.Identity{ExecutionID: "33333333-3333-4333-8333-333333333333", Fence: 1},
		ProducerCheckpointID: "opaque-checkpoint",
		Incarnation: output.SourceIncarnation{
			ExecutionID:      "33333333-3333-4333-8333-333333333333",
			NodeUID:          "node-1",
			HandleGeneration: 3,
			Output:           "result",
		},
		Output:      "result",
		WriterFence: 9,
	}
	if _, _, err := daemon.StatExact(context.Background(), valid(), claims); err != nil {
		t.Fatalf("the control challenge was refused: %v", err)
	}

	for _, row := range []struct {
		name    string
		mutate  func(*output.StatChallenge)
		sentine error
		says    []string
	}{
		{
			// A challenge minted under the next epoch, offered to the daemon
			// still holding this one's private key. Signing it would produce a
			// receipt claiming an epoch whose pinned public key cannot check it.
			name:    "a challenge from another activation epoch",
			mutate:  func(c *output.StatChallenge) { c.ActivationEpoch++ },
			sentine: output.ErrConflict,
			says:    []string{"8", "7"},
		},
		{
			// The marked half of "a fresh exact-generation marked stat": the
			// object at that exact generation has to be THIS capture's, not
			// merely present at the key.
			name:    "a challenge naming another capture's reservation",
			mutate:  func(c *output.StatChallenge) { c.ReservationID = somebodyElse },
			sentine: output.ErrConflict,
			says:    []string{"marked for reservation", string(mine), string(somebodyElse)},
		},
		{
			// The reachable variant of the ref check. A generation that is not
			// there fails in the stat itself, before any comparison, which is
			// why this row asserts ErrNotFound and not ErrConflict.
			name: "a challenge naming a generation that is not there",
			mutate: func(c *output.StatChallenge) {
				c.Ref.Generation = object.Attributes.Ref.Generation + 1000
			},
			sentine: output.ErrNotFound,
		},
	} {
		challenge := valid()
		row.mutate(&challenge)

		receipt, _, err := daemon.StatExact(context.Background(), challenge, claims)
		if !errors.Is(err, row.sentine) {
			t.Errorf("%s was answered with %v, expected %v", row.name, err, row.sentine)
		}
		if receipt.Signature != "" {
			t.Errorf("%s produced a signed receipt", row.name)
		}
		for _, fragment := range row.says {
			if err == nil || !strings.Contains(err.Error(), fragment) {
				t.Errorf("the refusal of %s does not name %q: %v", row.name, fragment, err)
			}
		}
	}
}
