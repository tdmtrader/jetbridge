package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The route table, driven over real HTTP against the real ledgers.
//
// What is under test is the boundary rather than the ledgers: which facet a
// route admits, what a replayed capability does, what an unready daemon
// answers, and whether a base request can be made to mention an output.

type routeFixture struct {
	*sourceFixture

	server *httptest.Server
	daemon *Daemon
	minter *executioncontrol.CapabilityMinter
	epoch  executioncontrol.ActivationEpoch
	nonce  int
}

func newRoutes(t *testing.T, unready string) *routeFixture {
	t.Helper()

	source := newSourceLedger(t)

	emulatorServer, bucket := emulator(t)
	config := validConfig(t, emulatorServer.URL(), bucket)
	// The daemon signs with the SAME control key the fixture's ledgers hold, so
	// a statement a route returns verifies under the key the test pinned. Two
	// keys here would make the assertions test the fixture's plumbing rather
	// than the daemon's.
	config.ControlKeyFile = writePrivateKey(t, source.private)
	daemon, err := Build(t.Context(), config)
	if err != nil {
		t.Fatalf("building the daemon: %v", err)
	}

	secret := bytes.Repeat([]byte{7}, executioncontrol.CapabilityKeyBytes)
	minter, err := executioncontrol.NewCapabilityMinter(secret, time.Minute, source.clock)
	if err != nil {
		t.Fatalf("building the minter: %v", err)
	}
	verifier, err := executioncontrol.NewCapabilityVerifier(secret, time.Minute, source.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	fixture := &routeFixture{
		sourceFixture: source,
		daemon:        daemon,
		minter:        minter,
		epoch:         daemon.Namespace().ActivationEpoch(),
	}
	fixture.server = httptest.NewServer(
		NewServer(daemon, source.ledger, source.source, verifier, unready).Handler())
	t.Cleanup(fixture.server.Close)

	return fixture
}

// call presents a capability minted for exactly the facet and operation given,
// which is what makes a cross-facet row a real cross-facet row: the token is
// valid, it is simply not for this route.
func (fixture *routeFixture) call(t *testing.T, path string, facet executioncontrol.Facet,
	operation string, body any) (int, []byte) {
	t.Helper()

	fixture.nonce++
	token, err := fixture.minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           facet,
		Operation:       operation,
		Identity:        identity(1),
		ActivationEpoch: fixture.epoch,
	}, "nonce-"+strings.ReplaceAll(path, "/", "-")+"-"+operation+"-"+strconv.Itoa(fixture.nonce))
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	return fixture.callWith(t, path, token, body)
}

func (fixture *routeFixture) callWith(t *testing.T, path string,
	token executioncontrol.ControlCapability, body any) (int, []byte) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set(CapabilityHeader, string(token))

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("calling %s: %v", path, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	return response.StatusCode, answer
}

// A base control capability cannot hold, seal or publish.
//
// The control is the SAME token succeeding at its own operation, asserted
// first, so this cannot go green on a daemon that rejects the token outright.
func TestABaseControlCapabilityCannotHoldSealOrPublish(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	// The control.
	status, body := fixture.call(t, "/execution/v1/classify",
		executioncontrol.BaseFacet, "classify", identified{Execution: identity(1)})
	if status != http.StatusOK {
		t.Fatalf("the base capability was refused at its own operation: %d %s", status, body)
	}

	// And now the same facet, at the extension's routes.
	for path, operation := range map[string]string{
		"/capture/v1/hold":          "hold",
		"/capture/v1/writer-ticket": "issue-writer-ticket",
		"/capture/v1/seal":          "begin-seal",
		"/capture/v1/publish":       "publish",
		"/capture/v1/release":       "release-hold",
	} {
		status, body := fixture.call(t, path, executioncontrol.BaseFacet, operation,
			identified{Execution: identity(1)})
		if status != http.StatusForbidden {
			t.Errorf("a base control capability was admitted at %s: %d %s", path, status, body)
		}
		if !strings.Contains(string(body), string(output.CaptureFacet)) {
			t.Errorf("the refusal at %s does not name the facet it wanted: %s", path, body)
		}
	}

	// The mirror: a capture capability at a base route.
	status, body = fixture.call(t, "/execution/v1/classify",
		output.CaptureFacet, "classify", identified{Execution: identity(1)})
	if status != http.StatusForbidden {
		t.Errorf("a capture capability was admitted at a base route: %d %s", status, body)
	}
}

// A capability authorizes one operation.
func TestAReplayedCapabilityIsRefusedAndAFreshOneIsNot(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	token, err := fixture.minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           executioncontrol.BaseFacet,
		Operation:       "classify",
		Identity:        identity(1),
		ActivationEpoch: fixture.epoch,
	}, "nonce-replayed")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identified{Execution: identity(1)}); status != http.StatusOK {
		t.Fatalf("the first presentation was refused: %d %s", status, body)
	}
	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identified{Execution: identity(1)}); status != http.StatusForbidden {
		t.Errorf("a replayed capability was admitted: %d %s", status, body)
	}

	// A fresh one still works, so the refusal is about this nonce and not about
	// the daemon having given up.
	if status, body := fixture.call(t, "/execution/v1/classify",
		executioncontrol.BaseFacet, "classify", identified{Execution: identity(1)}); status != http.StatusOK {
		t.Errorf("a fresh capability was refused after a replay: %d %s", status, body)
	}
}

// The base surface never requires or synthesizes an output or source field.
//
// This is decision F13's contract obligation stated where it can fail: a base
// caller sends an identity and gets a classification, and nothing on the way
// there or back mentions a hold, a capture, a bucket or a receipt.
func TestTheBaseSurfaceNeverMentionsTheExtension(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}

	for path, operation := range map[string]string{
		"/execution/v1/classify":         "classify",
		"/execution/v1/observe":          "observe",
		"/execution/v1/cleanup-eligible": "cleanup-eligible",
	} {
		status, body := fixture.call(t, path, executioncontrol.BaseFacet, operation,
			identified{Execution: identity(1)})
		if status != http.StatusOK {
			t.Fatalf("%s answered %d: %s", path, status, body)
		}
		for _, forbidden := range []string{
			"capture", "handoff", "source_lease", "incarnation", "bucket", "scope",
			"digest", "receipt", "writer_ticket",
		} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s answered with %q in it: %s", path, forbidden, body)
			}
		}
	}
}

// An unready daemon fails closed on every control route.
//
// Output-daemon unavailability never grants destructive authority, and
// "unavailable" includes "cannot read its own ledger".
func TestAnUnreadyDaemonAnswersNoControlRequestAtAll(t *testing.T) {
	// The control: the same fixture, ready, serves.
	ready := newRoutes(t, "")
	admitted(t, &ready.ledgerFixture)
	if status, body := ready.call(t, "/execution/v1/cleanup-eligible",
		executioncontrol.BaseFacet, "cleanup-eligible",
		identified{Execution: identity(1)}); status != http.StatusOK {
		t.Fatalf("the ready daemon refused: %d %s", status, body)
	}

	fixture := newRoutes(t, "the control ledger quarantined a record")
	admitted(t, &fixture.ledgerFixture)

	for path, operation := range map[string]string{
		"/execution/v1/classify":         "classify",
		"/execution/v1/cleanup-eligible": "cleanup-eligible",
		"/capture/v1/hold":               "hold",
	} {
		facet := executioncontrol.BaseFacet
		if strings.HasPrefix(path, "/capture/") {
			facet = output.CaptureFacet
		}
		status, body := fixture.call(t, path, facet, operation, identified{Execution: identity(1)})
		if status != http.StatusServiceUnavailable {
			t.Errorf("an unready daemon answered %s with %d: %s", path, status, body)
		}
		if !strings.Contains(string(body), "quarantined") {
			t.Errorf("the refusal at %s does not say why: %s", path, body)
		}
	}

	// Readiness itself says so, and liveness does not: a daemon whose ledger is
	// quarantined must stay alive so an operator can read the quarantine.
	live, err := http.Get(fixture.server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer live.Body.Close()
	if live.StatusCode != http.StatusOK {
		t.Errorf("an unready daemon reported itself dead: %d", live.StatusCode)
	}
	rdy, err := http.Get(fixture.server.URL + "/readyz")
	if err != nil {
		t.Fatalf("readyz: %v", err)
	}
	defer rdy.Body.Close()
	if rdy.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a daemon with a quarantined ledger reported ready: %d", rdy.StatusCode)
	}
}

// The whole capture chain over HTTP, ending in a marked object and a receipt.
func TestTheCaptureRoutesHoldSealAndPublishASealedTree(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	status, body := fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", admission())
	if status != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}
	var hold output.CaptureAcknowledgement
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatalf("decoding the hold: %v", err)
	}
	if err := output.VerifyCaptureAcknowledgement(hold, fixture.public); err != nil {
		t.Errorf("the hold the route returned does not verify: %v", err)
	}

	// A producer writes into the source the daemon issued.
	root, err := fixture.source.ResolveIncarnation(hold.Incarnation)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.txt"), []byte("the bytes"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// Publishing before the seal is refused: no canonical read begins before
	// both halves of the seal hold.
	publication := output.PublicationRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		ReservationID:   "44444444-4444-4444-8444-444444444444",
		CaptureFence:    captureFence,
	}
	if status, body := fixture.call(t, "/capture/v1/publish",
		output.CaptureFacet, "publish", publication); status != http.StatusPreconditionFailed {
		t.Errorf("an unsealed source was published: %d %s", status, body)
	}

	sealed := output.SealRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		Incarnation:     hold.Incarnation,
		CaptureFence:    captureFence,
		DeadlineAt:      output.NewTimestamp(fixedNow().Add(time.Hour)),
	}
	if status, body := fixture.call(t, "/capture/v1/seal",
		output.CaptureFacet, "begin-seal", sealed); status != http.StatusOK {
		t.Fatalf("the seal was refused: %d %s", status, body)
	}
	started, err := fixture.source.InspectSeal(testHandoff)
	if err != nil {
		t.Fatalf("inspecting the seal: %v", err)
	}
	if _, err := fixture.source.ConfirmSeal(t.Context(), output.SealConfirmation{
		Started:      started,
		CaptureFence: captureFence,
		ObservedAt:   output.NewTimestamp(fixedNow()),
	}); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	status, body = fixture.call(t, "/capture/v1/publish",
		output.CaptureFacet, "publish", publication)
	if status != http.StatusOK {
		t.Fatalf("the sealed tree was not published: %d %s", status, body)
	}
	var result output.PublicationResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if result.MarkerVersion != output.MarkerVersion {
		t.Errorf("the object is marked %q", result.MarkerVersion)
	}
	if result.Ref.Generation <= 0 {
		t.Error("the published object has no store-assigned generation")
	}
	if result.Deduplicated {
		t.Error("the first publication reported deduplication")
	}
	// The scope is the DERIVED one, not anything the request said -- the
	// request has no field that could have said it.
	if result.Ref.Scope != fixture.daemon.Namespace().Scope() {
		t.Errorf("the object landed in scope %q", result.Ref.Scope)
	}
}

// A caller-supplied bucket, scope or key is refused, and the server-derived one
// is served. The control is the line above: the same publish with no
// caller-chosen field succeeds.
func TestACallerSuppliedNamespaceIsRefusedByThePublishRoute(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	status, body := fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", admission())
	if status != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}
	var hold output.CaptureAcknowledgement
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	root, err := fixture.source.ResolveIncarnation(hold.Incarnation)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.txt"), []byte("the bytes"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if status, body := fixture.call(t, "/capture/v1/seal", output.CaptureFacet, "begin-seal",
		output.SealRequest{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       identity(1),
			ActivationEpoch: fixture.epoch,
			HandoffID:       testHandoff,
			Incarnation:     hold.Incarnation,
			CaptureFence:    captureFence,
			DeadlineAt:      output.NewTimestamp(fixedNow().Add(time.Hour)),
		}); status != http.StatusOK {
		t.Fatalf("sealing: %d %s", status, body)
	}
	started, err := fixture.source.InspectSeal(testHandoff)
	if err != nil {
		t.Fatalf("inspecting: %v", err)
	}
	if _, err := fixture.source.ConfirmSeal(t.Context(), output.SealConfirmation{
		Started: started, CaptureFence: captureFence, ObservedAt: output.NewTimestamp(fixedNow()),
	}); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	publication := output.PublicationRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		ReservationID:   "44444444-4444-4444-8444-444444444444",
		CaptureFence:    captureFence,
	}

	// The control, first.
	if status, body := fixture.call(t, "/capture/v1/publish",
		output.CaptureFacet, "publish", publication); status != http.StatusOK {
		t.Fatalf("the publication with no caller-chosen field was refused: %d %s", status, body)
	}

	for field, chosen := range map[string]output.CallerNamespaceRequest{
		"bucket": {Bucket: "somebody-elses-bucket"},
		"scope":  {Scope: "o0000000000000000000000000000000000000000"},
		"key":    {Key: "hangar/v1/scopes/x/trees/sha256/dead.tar.zst"},
		"prefix": {Prefix: "deployments/red"},
	} {
		carrying := publication
		carrying.Namespace = chosen

		status, body := fixture.call(t, "/capture/v1/publish",
			output.CaptureFacet, "publish", carrying)
		if status != http.StatusForbidden {
			t.Errorf("a publication naming a caller-chosen %s was answered %d: %s",
				field, status, body)
		}
		if !strings.Contains(string(body), field) {
			t.Errorf("the refusal for a caller-chosen %s does not name the field: %s", field, body)
		}
	}
}

// The handshake is the authority on what this daemon speaks. Node labels are
// hints; this is the thing a control plane reads before trusting a statement.
func TestTheHandshakeNamesTheProtocolLedgerKeyAndEpoch(t *testing.T) {
	fixture := newRoutes(t, "")

	response, err := http.Get(fixture.server.URL + "/handshake")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	var handshake executioncontrol.Handshake
	if err := json.Unmarshal(body, &handshake); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if err := handshake.Validate(); err != nil {
		t.Errorf("the handshake does not validate: %v", err)
	}
	if handshake.ActivationEpoch != fixture.epoch {
		t.Errorf("the handshake reports epoch %d", handshake.ActivationEpoch)
	}
	if handshake.ControlKeyID != "control-key-1" {
		t.Errorf("the handshake reports control key %q", handshake.ControlKeyID)
	}
	// It says nothing about any execution, which is why it needs no
	// capability: a handshake that leaked one would be an unauthenticated read
	// of this node's ledger.
	// The protocol version legitimately contains the word "execution", so the
	// list is of FIELDS rather than of substrings.
	for _, forbidden := range []string{"execution_id", "handoff", "bucket", "incarnation", "fence"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the unauthenticated handshake mentions %q: %s", forbidden, body)
		}
	}
}
