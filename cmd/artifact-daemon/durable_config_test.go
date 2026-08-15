package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// Backend selection is an operator-facing contract: asking for no durable
// store must stay disabled, each named backend must select that implementation,
// and incomplete configuration must fail startup rather than silently becoming
// a cold cache.
func TestBuildDurableTierSelectsAndValidatesBackend(t *testing.T) {
	logger := lagertest.NewTestLogger("durable-config")
	metrics := newMetrics()
	base := durableOptions{timeout: time.Second, maxBytes: 1024}

	t.Run("disabled", func(t *testing.T) {
		tier, err := buildDurableTier(logger, metrics, base)
		if err != nil || tier != nil {
			t.Fatalf("disabled durable tier = (%T, %v), want (nil, nil)", tier, err)
		}
	})

	t.Run("filesystem requires a path", func(t *testing.T) {
		opts := base
		opts.kind = "filesystem"
		if _, err := buildDurableTier(logger, metrics, opts); err == nil || !strings.Contains(err.Error(), "--durable-path") {
			t.Fatalf("filesystem without path error = %v", err)
		}
	})

	t.Run("filesystem rejects a path below a regular file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parent, []byte("file"), 0o644); err != nil {
			t.Fatalf("write invalid durable parent: %v", err)
		}
		opts := base
		opts.kind = "filesystem"
		opts.path = filepath.Join(parent, "objects")
		if _, err := buildDurableTier(logger, metrics, opts); err == nil {
			t.Fatal("filesystem accepted a durable path below a regular file")
		}
	})

	t.Run("gcs requires a bucket", func(t *testing.T) {
		opts := base
		opts.kind = "gcs"
		if _, err := buildDurableTier(logger, metrics, opts); err == nil || !strings.Contains(err.Error(), "--durable-bucket") {
			t.Fatalf("GCS without bucket error = %v", err)
		}
	})

	t.Run("gcs", func(t *testing.T) {
		opts := base
		opts.kind = "gcs"
		opts.bucket = "artifact-cache"
		// An endpoint override explicitly selects the credential-free emulator
		// protocol. Construction performs no request, so no server is needed.
		opts.endpoint = "http://127.0.0.1:1"
		tier, err := buildDurableTier(logger, metrics, opts)
		if err != nil {
			t.Fatalf("build GCS tier: %v", err)
		}
		if _, ok := tier.ObjectStore().(*durable.GCS); !ok {
			t.Fatalf("GCS selection built %T", tier.ObjectStore())
		}
	})

	t.Run("s3 requires a bucket", func(t *testing.T) {
		opts := base
		opts.kind = "s3"
		if _, err := buildDurableTier(logger, metrics, opts); err == nil || !strings.Contains(err.Error(), "--durable-bucket") {
			t.Fatalf("S3 without bucket error = %v", err)
		}
	})

	t.Run("s3", func(t *testing.T) {
		// Keep SDK construction deterministic and local. These are the same
		// standard-chain credentials an S3-compatible deployment supplies.
		t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

		opts := base
		opts.kind = "s3"
		opts.bucket = "artifact-cache"
		opts.endpoint = "http://127.0.0.1:1"
		opts.region = "us-east-1"
		tier, err := buildDurableTier(logger, metrics, opts)
		if err != nil {
			t.Fatalf("build S3 tier: %v", err)
		}
		if _, ok := tier.ObjectStore().(*durable.S3); !ok {
			t.Fatalf("S3 selection built %T", tier.ObjectStore())
		}
	})

	t.Run("unknown backend", func(t *testing.T) {
		opts := base
		opts.kind = "tape"
		if _, err := buildDurableTier(logger, metrics, opts); err == nil || !strings.Contains(err.Error(), "unknown --durable-store") {
			t.Fatalf("unknown backend error = %v", err)
		}
	})
}
