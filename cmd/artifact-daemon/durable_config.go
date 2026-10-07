package main

import (
	"context"
	"sync"
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

// openDurableStore is durable.Open, replaceable in tests.
var openDurableStore = durable.Open

// Reconnection backoff for a cache store unreachable at startup.
var (
	durableConnectInitialBackoff = 5 * time.Second
	durableConnectMaxBackoff     = 5 * time.Minute
)

// buildDurableTier turns the flags into a tier, or (nil, nil) when the operator
// did not ask for one.
//
// A misconfiguration is an error rather than a silent fallback to disabled.
// Getting a cold cache for months because a bucket name had a typo is a much
// worse failure than refusing to start. That includes a store that ANSWERS
// and refuses: a rejected credential or a store reporting another identity is
// configuration, and the daemon exits on it.
//
// A store that is configured correctly but cannot be reached at startup is NOT
// an error: the tier is fail-open, and a cache outage must never stop the
// daemon serving builds. The tier is returned unconnected (every operation a
// miss, not advertised to the ATC) and connects in the background, retrying
// with backoff until ctx is done. onConnect runs once the store is live -- the
// caller starts its retention pass there. The returned closer closes whatever
// store ends up connected.
func buildDurableTier(ctx context.Context, logger lager.Logger, m *metrics, opts durableOptions, onConnect func(*DurableTier)) (*DurableTier, func() error, error) {
	noop := func() error { return nil }
	if opts.kind == "" {
		return nil, noop, nil
	}
	config := opts.config()
	if err := config.Validate(); err != nil {
		return nil, nil, err
	}

	tier := NewDurableTier(logger, nil, m, opts.timeout)
	var closeMu sync.Mutex
	closeStore := noop
	closer := func() error {
		closeMu.Lock()
		defer closeMu.Unlock()
		return closeStore()
	}
	install := func(store durable.Store, closeIt func() error) {
		closeMu.Lock()
		closeStore = closeIt
		closeMu.Unlock()
		tier.connect(store)
		if onConnect != nil {
			onConnect(tier)
		}
	}

	store, closeIt, err := openDurableStore(ctx, config)
	if err == nil {
		install(store, closeIt)
		return tier, closer, nil
	}
	if !durable.IsUnavailable(err) {
		return nil, nil, err
	}

	logger.Error("durable-store-unavailable", err, lager.Data{
		"note": "the resource cache tier is off and retrying in the background; builds re-download meanwhile",
	})
	m.recordDurable("open", "error")
	go func() {
		backoff := durableConnectInitialBackoff
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			store, closeIt, err := openDurableStore(ctx, config)
			if err == nil {
				logger.Info("durable-store-connected")
				install(store, closeIt)
				return
			}
			if !durable.IsUnavailable(err) {
				// It answered and refused. Startup would have exited on this;
				// mid-run the cache simply stays off, loudly.
				logger.Error("durable-store-refused", err, lager.Data{
					"note": "the cache store answered and refused this configuration; the tier stays off",
				})
				return
			}
			m.recordDurable("open", "error")
			backoff = min(backoff*2, durableConnectMaxBackoff)
		}
	}()

	return tier, closer, nil
}

// hangarInputNamespace is the strict-input namespace this daemon will use, or
// empty when strict inputs are off and the flag is inert.
func hangarInputNamespace(enabled bool, bucket string) string {
	if !enabled {
		return ""
	}
	return bucket
}
