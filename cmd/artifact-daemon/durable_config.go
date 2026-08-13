package main

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// durableOptions is the flag surface for the long-term store, gathered into
// one struct so main() stays readable.
type durableOptions struct {
	kind     string
	path     string
	bucket   string
	prefix   string
	endpoint string
	region   string
	timeout  time.Duration
	maxBytes int64
}

// buildDurableTier turns the flags into a tier, or (nil, nil) when the operator
// did not ask for one.
//
// A misconfiguration is an error rather than a silent fallback to disabled.
// Getting a cold cache for months because a bucket name had a typo is a much
// worse failure than refusing to start.
func buildDurableTier(logger lager.Logger, m *metrics, opts durableOptions) (*DurableTier, error) {
	var store durable.Store

	switch opts.kind {
	case "":
		return nil, nil

	case "filesystem":
		if opts.path == "" {
			return nil, fmt.Errorf("--durable-path is required for --durable-store=filesystem")
		}
		fs, err := durable.NewFS(opts.path, opts.maxBytes)
		if err != nil {
			return nil, err
		}
		store = fs

	case "gcs":
		if opts.bucket == "" {
			return nil, fmt.Errorf("--durable-bucket is required for --durable-store=gcs")
		}
		// No credential flags: on GKE the right answer is Workload Identity
		// via Application Default Credentials, and off GKE the SDK already
		// reads GOOGLE_APPLICATION_CREDENTIALS. Neither needs a key here.
		gcs, err := durable.NewGCS(context.Background(), durable.GCSConfig{
			Bucket:   opts.bucket,
			Prefix:   opts.prefix,
			Endpoint: opts.endpoint,
			Limit:    opts.maxBytes,
		})
		if err != nil {
			return nil, err
		}
		store = gcs

	case "s3":
		if opts.bucket == "" {
			return nil, fmt.Errorf("--durable-bucket is required for --durable-store=s3")
		}
		// Credentials come from the SDK's default chain, which is what picks
		// up IRSA and Workload Identity. Nothing here reads a key from a flag:
		// flags land in the process table and in `kubectl describe`.
		s3, err := durable.NewS3(context.Background(), durable.S3Config{
			Bucket:   opts.bucket,
			Prefix:   opts.prefix,
			Endpoint: opts.endpoint,
			Region:   opts.region,
			Limit:    opts.maxBytes,
		})
		if err != nil {
			return nil, err
		}
		store = s3

	default:
		return nil, fmt.Errorf("unknown --durable-store %q: want \"\", \"gcs\", \"s3\" or \"filesystem\"", opts.kind)
	}

	return NewDurableTier(logger, store, m, opts.timeout), nil
}
