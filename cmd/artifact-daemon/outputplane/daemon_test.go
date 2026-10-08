package outputplane

import (
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
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// The daemon's construction and its publish path.
//
// stdlib testing, matching cmd/artifact-daemon (0 Ginkgo / 45 stdlib), and a
// real emulator rather than a stub store: what is under test is a binary that
// talks to a bucket, and a construction test against a double would prove the
// double was constructible.

func writeSigningKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key pair: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("encoding the private key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "signing.pem")
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

	controlKeyFile, _ := writeSigningKey(t)
	materializeKeyFile := writeMaterializationKey(t)

	return Config{
		OutputStore:       output.StoreGCS,
		OutputEndpoint:    endpoint,
		OutputBucket:      bucket,
		OutputPrefix:      "deployments/blue",
		OutputTenant:      "tenant-a",
		CacheBucket:       "deployment-durable-cache",
		StrictInputBucket: "deployment-strict-input",

		MaterializationKeyID:   "materialize-key-1",
		MaterializationKeyFile: materializeKeyFile,
		ControlKeyID:           "control-key-1",
		ControlKeyFile:         controlKeyFile,
		NodeUID:                "node-1",
		ScratchDir:             t.TempDir(),
		CapabilityTTL:          time.Minute,
		ActivationEpoch:        7,
		PublishConcurrency:     1,
		OperationTimeout:       10 * time.Second,
	}
}

func TestTheDaemonRefusesToBePointedAtAnotherPlanesBucket(t *testing.T) {
	server, bucket := emulator(t)

	// The control, first: the valid configuration builds.
	if _, err := Build(context.Background(), validConfig(t, server.URL(), bucket)); err != nil {
		t.Fatalf("the valid configuration did not build: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"the durable cache bucket": func(c *Config) { c.OutputBucket = c.CacheBucket },
		"the strict-input bucket":  func(c *Config) { c.OutputBucket = c.StrictInputBucket },
		"a filesystem store":       func(c *Config) { c.OutputStore = "filesystem" },
		"an S3-compatible store":   func(c *Config) { c.OutputStore = "s3" },
		"no bucket":                func(c *Config) { c.OutputBucket = "" },
		"no tenant":                func(c *Config) { c.OutputTenant = "" },
		"no epoch":                 func(c *Config) { c.ActivationEpoch = 0 },
		"no control key id":        func(c *Config) { c.ControlKeyID = "" },
		"no control key file":      func(c *Config) { c.ControlKeyFile = "" },
		// One key for both would mean rotating either rotates both.
		"one key for read warrants and control": func(c *Config) { c.ControlKeyFile = c.MaterializationKeyFile },
		"a non-positive timeout":                func(c *Config) { c.OperationTimeout = 0 },
	} {
		config := validConfig(t, server.URL(), bucket)
		mutate(&config)

		if _, err := Build(context.Background(), config); err == nil {
			t.Errorf("the daemon was built with %s", name)
		}
	}
}

func TestTheDaemonSignsWithAnEd25519KeyAndNothingElse(t *testing.T) {
	server, bucket := emulator(t)

	// An RSA key is the interesting refusal: it parses as a PKCS#8 private key
	// and would sign perfectly well, producing statements no verifier in this
	// deployment can check.
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
	config.ControlKeyFile = path
	if _, err := Build(context.Background(), config); !errors.Is(err, output.ErrUnsupportedProtocol) {
		t.Errorf("the daemon accepted an RSA control key: %v", err)
	}

	// And a file that is not PEM at all.
	notPEM := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(notPEM, []byte("this is not a key"), 0o600); err != nil {
		t.Fatalf("writing the garbage key: %v", err)
	}
	config.ControlKeyFile = notPEM
	if _, err := Build(context.Background(), config); !errors.Is(err, output.ErrCorrupt) {
		t.Errorf("the daemon accepted a key file that is not PEM: %v", err)
	}
}

func TestACallerChosenNamespaceIsRefusedByThePublishPath(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "produced")
	fixture.pods.stop(testPod)
	sealed, err := sealNow(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	server, bucket := fixture.objects, fixture.bucket

	for field, chosen := range map[string]output.CallerNamespaceRequest{
		"bucket": {Bucket: "somebody-elses-bucket"},
		"scope":  {Scope: "o0000000000000000000000000000000000000000"},
		"key":    {Key: "hangar/v1/scopes/x/trees/sha256/dead.tar.zst"},
		"prefix": {Prefix: "deployments/red"},
	} {
		_, err := publishNow(t, fixture, output.CapturePublishRequest{
			ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
			Digest: sealed.Digest, Namespace: chosen,
		})
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
	flags := flag.NewFlagSet("artifact-daemon", flag.ContinueOnError)
	BindFlags(flags, &config)

	for _, name := range []string{
		"output-store", "output-endpoint", "output-bucket", "output-prefix", "output-tenant",
		"cache-bucket", "strict-input-bucket",
		"activation-epoch", "output-timeout",
		"control-key-id", "control-key-file", "node-uid", "output-scratch-dir",
		"capability-key", "capability-ttl",
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
			t.Errorf("the output plane declares --%s. It has no cache client and no "+
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
	// operator provisions the dedicated bucket. So a bucket that does not exist must
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
	handle := reflect.TypeOf((*publisher.Store)(nil)).Elem()
	for index := 0; index < handle.NumMethod(); index++ {
		switch name := handle.Method(index).Name; name {
		case "DeleteExact", "List":
			t.Errorf("the publisher store this daemon holds offers %s. Its Pod's service "+
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

// writePrivateKey puts an existing key on disk in the form the daemon reads.
func writePrivateKey(t *testing.T, private ed25519.PrivateKey) string {
	t.Helper()

	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	path := filepath.Join(t.TempDir(), "control.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	return path
}

// writeMaterializationKey writes the exact 32 raw bytes an output read warrant is
// signed with. It is a third key on purpose: a warrant must not be signable by
// anything that can mint a publication receipt.
func writeMaterializationKey(t *testing.T) string {
	t.Helper()

	material := make([]byte, output.ReadWarrantKeyBytes)
	if _, err := rand.Read(material); err != nil {
		t.Fatalf("generating the read-warrant key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "materialize.key")
	if err := os.WriteFile(path, material, 0o600); err != nil {
		t.Fatalf("writing the read-warrant key: %v", err)
	}

	return path
}
