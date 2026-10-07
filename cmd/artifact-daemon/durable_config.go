package main

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/hangar/objectstore"
)

// durableOptions is the flag surface for the long-term store, gathered into
// one struct so main() stays readable.
type durableOptions struct {
	kind      string
	bucket    string
	endpoint  string
	storeID   string
	tokenFile string
	caCert    string
	timeout   time.Duration
	maxBytes  int64
}

func (opts durableOptions) config() durable.Config {
	return durable.Config{
		Kind: opts.kind, Bucket: opts.bucket, Endpoint: opts.endpoint,
		StoreID: opts.storeID, TokenFile: opts.tokenFile, CACert: opts.caCert,
		Timeout: opts.timeout, Limit: opts.maxBytes,
	}
}

// validateStorageNamespaces is the startup half of ADR-0002: the fail-open
// cache and the fail-closed strict-input store are two different buckets (GCS)
// or namespaces (disk), never one. It replaces the old rule that kept them apart
// by the depth of an object key.
//
// An empty name is a store this daemon does not use. The output namespace is
// checked here too once this daemon is handed one.
func validateStorageNamespaces(cache, input, output string) error {
	return objectstore.Namespaces{Cache: cache, Input: input, Output: output}.Validate()
}

// buildDurableTier turns the flags into a tier, or (nil, nil) when the operator
// did not ask for one.
//
// A misconfiguration is an error rather than a silent fallback to disabled.
// Getting a cold cache for months because a bucket name had a typo is a much
// worse failure than refusing to start.
//
// A store that is configured correctly but unreachable at startup is NOT an
// error: the disk adapter proves its credential against the store when it is
// built, and a cache outage must never stop the daemon serving builds. The tier
// is left off, said out loud, and the next restart tries again.
func buildDurableTier(ctx context.Context, logger lager.Logger, m *metrics, opts durableOptions) (*DurableTier, func() error, error) {
	if opts.kind == "" {
		return nil, func() error { return nil }, nil
	}
	config := opts.config()
	if err := config.Validate(); err != nil {
		return nil, nil, err
	}

	store, closeStore, err := durable.Open(ctx, config)
	if err != nil {
		logger.Error("durable-store-unavailable", err, lager.Data{
			"note": "the resource cache tier is off until the daemon restarts; builds re-download instead",
		})
		m.recordDurable("open", "error")
		return nil, func() error { return nil }, nil
	}

	return NewDurableTier(logger, store, m, opts.timeout), closeStore, nil
}

// hangarInputNamespace is the strict-input namespace this daemon will use, or
// empty when strict inputs are off and the flag is inert.
func hangarInputNamespace(enabled bool, bucket string) string {
	if !enabled {
		return ""
	}
	return bucket
}
