package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The two facets, and what a daemon that has only the base one may say.
//
// A base-only cohort is a real deployment: it is the one the sibling
// `exact_execution_control` track schedules onto, and Req 58's downgrade
// requires reaching it from a running output plane without taking exact process
// control away. So the output facet is OPTIONAL configuration in this binary
// rather than a precondition of starting it, and the boundary between "this
// daemon has no output bucket" and "this daemon refuses to publish" is a typed
// refusal rather than a nil dereference.

// baseOnlyConfig is validConfig with the whole output facet removed.
func baseOnlyConfig(t *testing.T) Config {
	t.Helper()

	config := validConfig(t, "", "")
	config.OutputBucket = ""
	config.OutputPrefix = ""
	config.OutputTenant = ""
	config.OutputEndpoint = ""
	config.CacheBucket = ""
	config.StrictInputBucket = ""
	config.ReceiptKeyID = ""
	config.ReceiptKeyFile = ""
	config.MaterializationKeyID = ""
	config.MaterializationKeyFile = ""

	return config
}

func TestABaseControlDaemonBuildsWithNoOutputFacetAtAll(t *testing.T) {
	daemon, err := Build(t.Context(), baseOnlyConfig(t))
	if err != nil {
		t.Fatalf("a base-control-only daemon did not build: %v", err)
	}

	if daemon.OutputEnabled() {
		t.Error("a daemon with no output bucket reports the output facet enabled")
	}
	if daemon.ActivationEpoch() != 7 {
		t.Errorf("the base facet's activation epoch is %d, not the configured 7",
			daemon.ActivationEpoch())
	}
	if daemon.ControlKeyID() == "" {
		t.Error("a base-control daemon has no control key id; an unsigned acknowledgement " +
			"is not proof")
	}

	// The control, so the row above is not "a daemon that builds from anything":
	// the output facet still builds when it is configured.
	server, bucket := emulator(t)
	withOutput, err := Build(t.Context(), validConfig(t, server.URL(), bucket))
	if err != nil {
		t.Fatalf("the output facet did not build: %v", err)
	}
	if !withOutput.OutputEnabled() {
		t.Error("a daemon with an output bucket reports the output facet disabled")
	}
}

// A half-configured output facet is not a base-only daemon. Reaching base-only
// by deleting one value would be a deployment that believes it is publishing.
func TestAHalfConfiguredOutputFacetIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"a prefix with no bucket":         func(c *Config) { c.OutputPrefix = "deployments/blue" },
		"a tenant with no bucket":         func(c *Config) { c.OutputTenant = "tenant-a" },
		"an endpoint with no bucket":      func(c *Config) { c.OutputEndpoint = "http://gcs.test" },
		"a receipt key id with no bucket": func(c *Config) { c.ReceiptKeyID = "receipt-1" },
		"a receipt key with no bucket": func(c *Config) {
			path, _ := writeReceiptKey(t)
			c.ReceiptKeyFile = path
		},
		"a materialization key id with no bucket": func(c *Config) {
			c.MaterializationKeyID = "materialize-1"
		},
		"a materialization key with no bucket": func(c *Config) {
			c.MaterializationKeyFile = writeMaterializationKey(t)
		},
	} {
		config := baseOnlyConfig(t)
		mutate(&config)

		if _, err := Build(t.Context(), config); err == nil {
			t.Errorf("a daemon was built with %s", name)
		}
	}
}

// Req 24 again, from the other side: a daemon that signs no receipt is given no
// private key to sign one with. A key mounted into a process that cannot need
// it is a key an exploit of that process gets for free.
func TestABaseOnlyDaemonHoldsNoReceiptPrivateKey(t *testing.T) {
	daemon, err := Build(t.Context(), baseOnlyConfig(t))
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if daemon.ReceiptSigner() != nil {
		t.Error("a base-control-only daemon holds a receipt signer")
	}
	if daemon.Publisher() != nil {
		t.Error("a base-control-only daemon holds a publisher; it has no bucket to publish into")
	}
}

// Req 58. Mixed or older components refuse durable output capture with a TYPED
// result and no cache-tier fallback. The control is first and in the same test:
// the base routes still answer, so "everything is refused" is not what this
// proves.
func TestABaseOnlyDaemonAnswersBaseRoutesAndTypedlyRefusesEveryCaptureRoute(t *testing.T) {
	fixture := newBaseOnlyRoutes(t)

	// The control.
	status, body := fixture.call(t, "/execution/v1/classify", executioncontrol.BaseFacet,
		"classify", identifiedBy(identity(1)))
	if status == http.StatusNotImplemented {
		t.Fatalf("a base route was refused as unimplemented: %d %s", status, body)
	}

	captureRoutes := []struct {
		path      string
		operation string
	}{
		{"/capture/v1/reserve-incarnation", "reserve-incarnation"},
		{"/capture/v1/hold", "hold"},
		{"/capture/v1/hold/inspect", "inspect-hold"},
		{"/capture/v1/writer-ticket", "issue-writer-ticket"},
		{"/capture/v1/writer-ticket/close", "close-writer-ticket"},
		{"/capture/v1/seal", "begin-seal"},
		{"/capture/v1/seal/confirm", "confirm-seal"},
		{"/capture/v1/seal/inspect", "inspect-seal"},
		{"/capture/v1/release", "release-hold"},
		{"/capture/v1/canonicalize", "canonicalize"},
		{"/capture/v1/publish", "publish"},
		{"/capture/v1/stat", "stat"},
	}
	if len(captureRoutes) < 12 {
		t.Fatalf("only %d capture routes are listed; the table drifted and this rule would "+
			"prove less than it says", len(captureRoutes))
	}

	for _, route := range captureRoutes {
		status, body := fixture.call(t, route.path, output.CaptureFacet, route.operation,
			map[string]any{"execution": identifiedBy(identity(1))})
		if status != http.StatusNotImplemented {
			t.Errorf("%s answered %d, not 501: a daemon with no output facet must refuse "+
				"capture, not attempt it\n%s", route.path, status, body)

			continue
		}
		var refusal struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &refusal); err != nil {
			t.Errorf("%s: the refusal is not the typed JSON shape: %v", route.path, err)

			continue
		}
		if !strings.Contains(refusal.Error, output.ErrCaptureDisabled.Error()) {
			t.Errorf("%s refused with %q, which is not the typed capture-disabled result",
				route.path, refusal.Error)
		}
		// Req 58: no cache-tier fallback. The refusal never mentions one.
		for _, forbidden := range []string{"cache", "fallback", "resource-cache"} {
			if strings.Contains(strings.ToLower(refusal.Error), forbidden) {
				t.Errorf("%s's refusal mentions %q; there is no cache-tier fallback and a "+
					"message suggesting one is how a caller writes the retry that takes it",
					route.path, forbidden)
			}
		}
	}
}

// Req 56. The extension handshake is what proves a cohort speaks capture, and
// it is only truthful where the facet exists.
func TestTheExtensionHandshakeIsServedOnlyWithTheOutputFacet(t *testing.T) {
	full := newRoutes(t, "")

	response, err := http.Get(full.server.URL + "/capture/v1/handshake")
	if err != nil {
		t.Fatalf("asking for the extension handshake: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the extension handshake answered %d", response.StatusCode)
	}
	var handshake output.ExtensionHandshake
	if err := json.NewDecoder(response.Body).Decode(&handshake); err != nil {
		t.Fatalf("decoding the extension handshake: %v", err)
	}
	if err := handshake.Validate(); err != nil {
		t.Errorf("the daemon's own extension handshake does not validate: %v", err)
	}
	if handshake.Base.ActivationEpoch != full.epoch {
		t.Errorf("the handshake reports epoch %d, the daemon publishes under %d",
			handshake.Base.ActivationEpoch, full.epoch)
	}
	if handshake.BucketFingerprint == full.bucket {
		t.Error("the handshake reports the bucket NAME as its fingerprint; a fingerprint is " +
			"what lets a control plane compare two cohorts without the name being the secret")
	}

	// And the base-only daemon, which must not answer it at all.
	baseOnly := newBaseOnlyRoutes(t)
	response, err = http.Get(baseOnly.server.URL + "/capture/v1/handshake")
	if err != nil {
		t.Fatalf("asking a base-only daemon for the extension handshake: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Errorf("a base-only daemon answered the extension handshake with %d", response.StatusCode)
	}

	// Its BASE handshake still answers: the cohort is attestable for exact
	// control while output_state is still initial, which is the whole reason
	// ExtensionHandshake embeds the base one rather than restating it.
	response, err = http.Get(baseOnly.server.URL + "/handshake")
	if err != nil {
		t.Fatalf("asking a base-only daemon for the base handshake: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("a base-only daemon refused the base handshake with %d", response.StatusCode)
	}
}

// newBaseOnlyRoutes serves a daemon whose output facet was never configured.
//
// It shares the route fixture's plumbing so the two are driven identically; the
// difference under test is the daemon, not the harness.
func newBaseOnlyRoutes(t *testing.T) *routeFixture {
	t.Helper()

	source := newSourceLedger(t)

	config := baseOnlyConfig(t)
	config.ControlKeyFile = writePrivateKey(t, source.private)
	daemon, err := Build(t.Context(), config)
	if err != nil {
		t.Fatalf("building a base-control-only daemon: %v", err)
	}

	minter, err := executioncontrol.NewCapabilityMinter(capabilitySecret(), time.Minute, source.clock)
	if err != nil {
		t.Fatalf("building the minter: %v", err)
	}

	fixture := &routeFixture{
		sourceFixture: source,
		daemon:        daemon,
		minter:        minter,
		epoch:         daemon.ActivationEpoch(),
		config:        config,
	}
	fixture.serveBaseOnly(t)

	return fixture
}

// serveBaseOnly is serve with a nil source ledger, which is what main.go builds
// when the output facet is off: the source ledger holds capture incarnations,
// and a daemon that captures nothing opens none.
func (fixture *routeFixture) serveBaseOnly(t *testing.T) {
	t.Helper()

	if fixture.server != nil {
		fixture.server.Close()
	}
	verifier, err := executioncontrol.NewCapabilityVerifier(
		capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	if err := verifier.RememberSpentIn(capabilityReplayStore{store: fixture.sourceFixture.store}); err != nil {
		t.Fatalf("opening the spent-capability record: %v", err)
	}
	fixture.server = httptest.NewServer(NewServer(fixture.daemon, fixture.ledger,
		nil, verifier, "").Handler())
	t.Cleanup(fixture.server.Close)
}
