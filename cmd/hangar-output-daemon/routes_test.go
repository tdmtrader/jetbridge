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

	"github.com/fsouza/fake-gcs-server/fakestorage"

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

	server  *httptest.Server
	unready string
	daemon  *Daemon
	minter  *executioncontrol.CapabilityMinter
	epoch   executioncontrol.ActivationEpoch
	nonce   int

	// The store this daemon was pointed at, and the configuration it was built
	// from. The redaction scan needs both: it has to know the bucket name and
	// the object keys before it can assert nothing the daemon says contains
	// them.
	store  *fakestorage.Server
	bucket string
	config Config

	// minted records every capability this fixture handed the daemon, so the
	// scan can assert none of them came back.
	minted []executioncontrol.ControlCapability
	// emitted records every response the daemon produced, for the same reason.
	emitted []string
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

	minter, err := executioncontrol.NewCapabilityMinter(capabilitySecret(), time.Minute, source.clock)
	if err != nil {
		t.Fatalf("building the minter: %v", err)
	}

	fixture := &routeFixture{
		sourceFixture: source,
		daemon:        daemon,
		minter:        minter,
		epoch:         daemon.Namespace().ActivationEpoch(),
		store:         emulatorServer,
		bucket:        bucket,
		config:        config,
		unready:       unready,
	}
	fixture.serve(t)

	return fixture
}

// capabilitySecret is the one key the minter and every verifier in these tests
// share.
func capabilitySecret() []byte {
	return bytes.Repeat([]byte{7}, executioncontrol.CapabilityKeyBytes)
}

// serve builds a verifier the way the daemon builds one and puts a server in
// front of it.
//
// It is called again by the restart row, which is the whole reason it is a
// method: what a restart must not lose is the set of capabilities already
// spent, and the only way to see that is to build a second verifier over the
// same control directory.
func (fixture *routeFixture) serve(t *testing.T) {
	t.Helper()

	if fixture.server != nil {
		fixture.server.Close()
	}
	verifier, err := executioncontrol.NewCapabilityVerifier(
		capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	// The same wiring main.go does: the spent nonces belong in the control
	// directory, not in one process's memory.
	if err := verifier.RememberSpentIn(capabilityReplayStore{store: fixture.sourceFixture.store}); err != nil {
		t.Fatalf("opening the spent-capability record: %v", err)
	}
	fixture.server = httptest.NewServer(NewServer(fixture.daemon, fixture.ledger,
		fixture.source, verifier, fixture.unready).Handler())
	t.Cleanup(fixture.server.Close)
}

// call presents a capability minted for exactly the facet and operation given,
// which is what makes a cross-facet row a real cross-facet row: the token is
// valid, it is simply not for this route.
// identifiedBy is the body a base route takes: the frozen types embed Identity,
// so the execution is flat rather than nested.
func identifiedBy(id executioncontrol.Identity) map[string]any {
	return map[string]any{"execution_id": id.ExecutionID, "fence": id.Fence}
}

func (fixture *routeFixture) call(t *testing.T, path string, facet executioncontrol.Facet,
	operation string, body any) (int, []byte) {
	t.Helper()

	return fixture.callAs(t, identity(1), path, facet, operation, body)
}

// callAs is call for a capability minted for an execution the test names.
//
// The default is the fixture's one execution; a cross-execution row needs a
// token that is valid for a DIFFERENT one, because the finding it is about is
// a route that verifies the capability against the identity in the body and
// then acts on a handoff that identity has nothing to do with.
func (fixture *routeFixture) callAs(t *testing.T, as executioncontrol.Identity, path string,
	facet executioncontrol.Facet, operation string, body any) (int, []byte) {
	t.Helper()

	fixture.nonce++
	token, err := fixture.minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           facet,
		Operation:       operation,
		Identity:        as,
		ActivationEpoch: fixture.epoch,
	}, "nonce-"+strings.ReplaceAll(path, "/", "-")+"-"+operation+"-"+strconv.Itoa(fixture.nonce))
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	fixture.minted = append(fixture.minted, token)

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
	fixture.emitted = append(fixture.emitted, path+" -> "+string(answer))

	return response.StatusCode, answer
}

// What the route table has that no scenario can reach.
//
// `A base control capability cannot hold, seal or publish` pins the facet rule
// over the wire, at hold, seal and publish, with the same token succeeding at
// its own operation first. This used to repeat all three of those rows and the
// checkpoint's clause says no Go test here duplicates an answer a scenario
// already pins, so what is left is the three things the scenario cannot say:
//
//   - the FLAT identity body. The base protocol's frozen types embed Identity,
//     so an Envelope carries execution_id and fence at the top level while the
//     extension's types nest them under `execution`. The middleware reads both,
//     and nothing else in the tree exercises the flat one.
//   - the MIRROR: a capture capability at a base route. The fixture speaks the
//     capture facet; it has no phrase for presenting one at /execution/v1.
//   - the two capture routes the scenario does not name -- the writer ticket
//     and the release -- because a facet check that was data per route could
//     be true of three routes and not of five.
func TestTheRouteTableReadsBothIdentityShapesAndNoFacetCrosses(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	// The control: the base capability at its own operation.
	if status, body := fixture.call(t, "/execution/v1/classify",
		executioncontrol.BaseFacet, "classify", identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the base capability was refused at its own operation: %d %s", status, body)
	}

	// The flat identity body, over HTTP.
	flattened := newRoutes(t, "")
	if status, body := flattened.call(t, "/execution/v1/admit",
		executioncontrol.BaseFacet, "admit", executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        identity(1),
			ActivationEpoch: flattened.epoch,
			NodeUID:         testNode,
			PodUID:          testPod,
			Capability:      "opaque-capability",
		}); status != http.StatusOK {
		t.Fatalf("the admit route refused an envelope with a flat identity: %d %s", status, body)
	}

	// The two capture routes no scenario names.
	for path, operation := range map[string]string{
		"/capture/v1/writer-ticket": "issue-writer-ticket",
		"/capture/v1/release":       "release-hold",
	} {
		status, body := fixture.call(t, path, executioncontrol.BaseFacet, operation,
			identifiedBy(identity(1)))
		if status != http.StatusForbidden {
			t.Errorf("a base control capability was admitted at %s: %d %s", path, status, body)
		}
		if !strings.Contains(string(body), string(output.CaptureFacet)) {
			t.Errorf("the refusal at %s does not name the facet it wanted: %s", path, body)
		}
	}

	// The mirror: a capture capability at a base route.
	if status, body := fixture.call(t, "/execution/v1/classify",
		output.CaptureFacet, "classify", identifiedBy(identity(1))); status != http.StatusForbidden {
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
		identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the first presentation was refused: %d %s", status, body)
	}
	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Errorf("a replayed capability was admitted: %d %s", status, body)
	}

	// A fresh one still works, so the refusal is about this nonce and not about
	// the daemon having given up.
	if status, body := fixture.call(t, "/execution/v1/classify",
		executioncontrol.BaseFacet, "classify", identifiedBy(identity(1))); status != http.StatusOK {
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
			identifiedBy(identity(1)))
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
		identifiedBy(identity(1))); status != http.StatusOK {
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
		status, body := fixture.call(t, path, facet, operation, identifiedBy(identity(1)))
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
	started, err := fixture.source.InspectSeal(testHandoff, identity(1))
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

// A capability minted for one execution must not act on another's handoff.
//
// The middleware reads the identity out of the body and checks the capability
// against it, and five capture routes then act on facts derived from that same
// identity. Three did not: inspect-hold and inspect-seal took the handoff
// straight out of the body, and confirm-seal read its execution out of the
// caller-supplied SealStarted rather than out of the identity the token was
// bound to. So execution B, holding nothing, presenting a capability minted
// for B, read A's hold and A's captured drain set -- and moved A's source to
// `sealed`, which is the state that admits a canonical read.
//
// The control is asserted first, and it is the same three routes answering for
// the execution they belong to.
func TestACapabilityForOneExecutionCannotActOnAnothersHandoff(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	// A holds and seals.
	status, body := fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", admission())
	if status != http.StatusOK {
		t.Fatalf("A's hold was refused: %d %s", status, body)
	}
	var hold output.CaptureAcknowledgement
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatalf("decoding: %v", err)
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
		t.Fatalf("A's seal was refused: %d %s", status, body)
	}
	started, err := fixture.source.InspectSeal(testHandoff, identity(1))
	if err != nil {
		t.Fatalf("inspecting A's seal: %v", err)
	}

	query := map[string]any{"execution": identity(1), "handoff_id": testHandoff}
	// The controls: A's own capability, at A's own handoff.
	for path, operation := range map[string]string{
		"/capture/v1/hold/inspect": "inspect-hold",
		"/capture/v1/seal/inspect": "inspect-seal",
	} {
		if status, body := fixture.call(t, path, output.CaptureFacet, operation,
			query); status != http.StatusOK {
			t.Fatalf("%s refused the execution it belongs to: %d %s", path, status, body)
		}
	}

	// B is a real, admitted execution on this node. It holds no source.
	b := executioncontrol.Identity{
		ExecutionID: "55555555-5555-4555-8555-555555555555", Fence: 1,
	}
	if err := fixture.ledger.Admit(executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        b,
		ActivationEpoch: testEpoch,
		NodeUID:         testNode,
		PodUID:          testPod,
		Capability:      "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting B: %v", err)
	}

	crossQuery := map[string]any{"execution": b, "handoff_id": testHandoff}
	crossConfirmation := map[string]any{
		"execution":     b,
		"started":       started,
		"capture_fence": captureFence,
		"observed_at":   output.NewTimestamp(fixedNow()),
	}

	for _, row := range []struct {
		path, operation string
		body            any
	}{
		{"/capture/v1/hold/inspect", "inspect-hold", crossQuery},
		{"/capture/v1/seal/inspect", "inspect-seal", crossQuery},
		{"/capture/v1/seal/confirm", "confirm-seal", crossConfirmation},
	} {
		status, body := fixture.callAs(t, b, row.path, output.CaptureFacet, row.operation, row.body)
		if status != http.StatusForbidden {
			t.Errorf("%s served execution B a fact about A's handoff: %d %s",
				row.path, status, body)
		}
	}

	// And A's source is still A's: not sealed by B, and A can still confirm.
	if _, err := fixture.source.ConfirmSeal(t.Context(), output.SealConfirmation{
		Started: started, CaptureFence: captureFence, ObservedAt: output.NewTimestamp(fixedNow()),
	}); err != nil {
		t.Errorf("A could not confirm its own seal afterwards: %v", err)
	}
}

// A capability is spent once, and a restart does not forget that.
//
// The replay refusal lived in the verifier's memory. A daemon restart -- a
// crash, a rollout, an OOM kill -- emptied it, so a token captured off the
// wire was admitted a second time inside its TTL, which is up to fifteen
// minutes. The bound was the ledgers' own idempotency rather than the
// capability, and the auth test said "replayed capability ... fail closed"
// without the row that a restart is.
//
// The pair: a fresh capability still works after the restart, so the refusal
// is about this nonce and not about the daemon having given up.
func TestASpentCapabilityIsStillSpentAfterARestart(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	token, err := fixture.minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           executioncontrol.BaseFacet,
		Operation:       "classify",
		Identity:        identity(1),
		ActivationEpoch: fixture.epoch,
	}, "nonce-spent-across-a-restart")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the first presentation was refused: %d %s", status, body)
	}

	// A nonce that has already expired, planted in the record. It cannot
	// authorize anything, and a record that kept them would grow with every
	// capability this node ever saw.
	spent := capabilityReplayStore{store: fixture.sourceFixture.store}
	nonces, err := spent.LoadSpentCapabilities()
	if err != nil {
		t.Fatalf("reading the spent record: %v", err)
	}
	if _, recorded := nonces["nonce-spent-across-a-restart"]; !recorded {
		t.Fatalf("the presented capability was not recorded as spent: %v", nonces)
	}
	nonces["nonce-from-an-hour-ago"] = fixedNow().Add(-time.Hour)
	if err := spent.SaveSpentCapabilities(nonces); err != nil {
		t.Fatalf("planting an expired nonce: %v", err)
	}

	// The restart: a new process over the same control directory.
	fixture.serve(t)

	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Errorf("a capability spent before the restart was admitted after it: %d %s", status, body)
	}
	if status, body := fixture.call(t, "/execution/v1/classify",
		executioncontrol.BaseFacet, "classify", identifiedBy(identity(1))); status != http.StatusOK {
		t.Errorf("a fresh capability was refused after the restart: %d %s", status, body)
	}

	kept, err := spent.LoadSpentCapabilities()
	if err != nil {
		t.Fatalf("re-reading the spent record: %v", err)
	}
	if _, stale := kept["nonce-from-an-hour-ago"]; stale {
		t.Error("an expired nonce survived the restart; the record grows without bound")
	}
	if _, recorded := kept["nonce-spent-across-a-restart"]; !recorded {
		t.Error("the spent nonce was pruned along with the expired one")
	}
}
