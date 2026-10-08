package outputplane

import (
	"context"
	"errors"
	"flag"
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

	return Config{
		OutputStore:       output.StoreGCS,
		OutputEndpoint:    endpoint,
		OutputBucket:      bucket,
		OutputPrefix:      "deployments/blue",
		OutputTenant:      "tenant-a",
		CacheBucket:       "deployment-durable-cache",
		StrictInputBucket: "deployment-strict-input",

		Key:                hangarKey(),
		NodeUID:            "node-1",
		ScratchDir:         t.TempDir(),
		PublishConcurrency: 1,
		OperationTimeout:   10 * time.Second,
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
		"no Hangar key":            func(c *Config) { c.Key = nil },
		"a short Hangar key":       func(c *Config) { c.Key = []byte("short") },
		"a non-positive timeout":   func(c *Config) { c.OperationTimeout = 0 },
	} {
		config := validConfig(t, server.URL(), bucket)
		mutate(&config)

		if _, err := Build(context.Background(), config); err == nil {
			t.Errorf("the daemon was built with %s", name)
		}
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
		"output-timeout", "node-uid", "output-scratch-dir",
	} {
		if flags.Lookup(name) == nil {
			t.Errorf("the daemon has no --%s flag", name)
		}
	}

	// And the flags it must NOT have. A durable-store flag here would be the
	// bucket isolation undone by a helm value; the Hangar key and the mount
	// switch are the daemon's own flags, loaded before the node is labelled,
	// and a second spelling of either here would be a second key path.
	for _, forbidden := range []string{
		"durable-store", "durable-bucket", "durable-endpoint", "durable-path",
		"storage-path", "hangar-enabled", "hangar-key", "execution-control",
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
