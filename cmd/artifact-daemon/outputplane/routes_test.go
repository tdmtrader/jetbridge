package outputplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/ledger"
)

// The route table, driven over real HTTP against the real ledgers.
//
// What is under test is the boundary rather than the ledgers: which warrant
// purpose a route admits, what a replayed warrant does, and whether a base
// request can be made to mention an output.

type routeFixture struct {
	*captureFixture

	server *httptest.Server
	api    *Server
	client *http.Client
	daemon *Daemon
	signer *hangar.Signer

	// The store this daemon was pointed at, and the configuration it was built
	// from. The redaction scan needs both: it has to know the bucket name and
	// the object keys before it can assert nothing the daemon says contains
	// them.
	store  *fakestorage.Server
	bucket string
	config Config

	// minted records every warrant this fixture handed the daemon, so the
	// scan can assert none of them came back.
	minted []string
	// emitted records every response the daemon produced, for the same reason.
	emitted []string
}

func newRoutes(t *testing.T) *routeFixture {
	t.Helper()

	capture := newCaptureLedger(t)
	signer, err := hangar.NewSigner(hangarKey(), time.Minute, capture.clock)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}

	fixture := &routeFixture{
		captureFixture: capture,
		daemon:         capture.daemon,
		signer:         signer,
		store:          capture.objects,
		bucket:         capture.bucket,
		config:         capture.config,
	}
	fixture.serve(t)

	return fixture
}

// hangarKey is the one Hangar key the signer and every verifier in these
// tests share.
func hangarKey() []byte {
	return bytes.Repeat([]byte{7}, hangar.WarrantKeyBytes)
}

// verifying builds the plane's verifier and spent set the way Open does: one
// verifier over the Hangar key, and the spent control nonces in the control
// directory rather than in one process's memory.
func (fixture *routeFixture) verifying(t *testing.T, store *controlStore) (*hangar.Verifier, *spentWarrants) {
	t.Helper()

	warrants, err := hangar.NewVerifier(hangarKey(), hangar.MaxWarrantTTL, fixture.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	spent, err := openSpentWarrants(store, fixture.clock)
	if err != nil {
		t.Fatalf("opening the spent-warrant record: %v", err)
	}

	return warrants, spent
}

// serve builds a verifier the way the daemon builds one and puts a server in
// front of it.
//
// It is called again by the restart row, which is the whole reason it is a
// method: what a restart must not lose is the set of warrants already spent,
// and the only way to see that is to build a second spent set over the same
// control directory.
func (fixture *routeFixture) serve(t *testing.T) {
	t.Helper()

	if fixture.server != nil {
		fixture.server.Close()
	}
	warrants, spent := fixture.verifying(t, fixture.captureFixture.store)
	fixture.api = NewServer(fixture.daemon, fixture.ledger, fixture.capture, warrants, spent)
	fixture.server = httptest.NewServer(fixture.api.Handler())
	t.Cleanup(fixture.server.Close)
}

// call presents a warrant minted for exactly the purpose and operation given,
// which is what makes a cross-purpose row a real cross-purpose row: the
// warrant is valid, it is simply not for this route.
// identifiedBy is the body a base route takes: the frozen types embed Identity,
// so the execution is flat rather than nested.
// sealOverHTTP asks the seal route until it stops answering 202.
func (fixture *routeFixture) sealOverHTTP(t *testing.T) (int, []byte) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		status, body := fixture.call(t, "/capture/v1/seal", hangar.PurposeControlCapture, "seal", sealRequest())
		if status != http.StatusAccepted || time.Now().After(deadline) {
			return status, body
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// publishOverHTTP asks the publish route until it stops answering 202.
func (fixture *routeFixture) publishOverHTTP(t *testing.T, body any) (int, []byte) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		status, answer := fixture.call(t, "/capture/v1/publish", hangar.PurposeControlCapture, "publish", body)
		if status != http.StatusAccepted || time.Now().After(deadline) {
			return status, answer
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func identifiedBy(id executioncontrol.Identity) map[string]any {
	return map[string]any{"execution_id": id.ExecutionID, "fence": id.Fence}
}

func (fixture *routeFixture) call(t *testing.T, path string, purpose hangar.Purpose,
	operation string, body any) (int, []byte) {
	t.Helper()

	return fixture.callAs(t, identity(1), path, purpose, operation, body)
}

// callAs is call for a warrant minted for an execution the test names.
//
// The default is the fixture's one execution; a cross-execution row needs a
// warrant that is valid for a DIFFERENT one, because the finding it is about
// is a route that verifies the warrant against the identity in the body and
// then acts on a handoff that identity has nothing to do with.
func (fixture *routeFixture) callAs(t *testing.T, as executioncontrol.Identity, path string,
	purpose hangar.Purpose, operation string, body any) (int, []byte) {
	t.Helper()

	token := fixture.mint(t, purpose, operation, as)

	return fixture.callWith(t, path, token, body)
}

// mint signs one control warrant and remembers it for the redaction scan.
func (fixture *routeFixture) mint(t *testing.T, purpose hangar.Purpose, operation string,
	as executioncontrol.Identity) string {
	t.Helper()

	token, err := fixture.signer.Sign(executioncontrol.ControlWarrant(purpose, operation, as))
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	fixture.minted = append(fixture.minted, token)

	return token
}

func (fixture *routeFixture) callWith(t *testing.T, path string,
	token string, body any) (int, []byte) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set(WarrantHeader, token)

	client := http.DefaultClient
	if fixture.client != nil {
		client = fixture.client
	}
	response, err := client.Do(request)
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
// `A base control capability cannot hold, seal or publish` pins the purpose
// rule over the wire, at hold, seal and publish, with the same warrant
// succeeding at its own operation first. This used to repeat all three of those rows and the
// checkpoint's clause says no Go test here duplicates an answer a scenario
// already pins, so what is left is the three things the scenario cannot say:
//
//   - the FLAT identity body. The base protocol's frozen types embed Identity,
//     so an Envelope carries execution_id and fence at the top level while the
//     extension's types nest them under `execution`. The middleware reads both,
//     and nothing else in the tree exercises the flat one.
//   - the MIRROR: an output-capture warrant at a base route. The fixture speaks
//     the capture extension; it has no phrase for presenting one at /execution/v1.
//   - every capture route, because a purpose check that was data per route
//     could be true of three routes and not of five.
func TestTheRouteTableReadsBothIdentityShapesAndNoPurposeCrosses(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	// The control: the execution-control warrant at its own operation.
	if status, body := fixture.call(t, "/execution/v1/classify",
		hangar.PurposeControlBase, "classify", identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the execution-control warrant was refused at its own operation: %d %s", status, body)
	}

	// The flat identity body, over HTTP.
	flattened := newRoutes(t)
	if status, body := flattened.call(t, "/execution/v1/admit",
		hangar.PurposeControlBase, "admit", executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        identity(1),
			NodeUID:         testNode,
			Capability:      "opaque-capability",
		}); status != http.StatusOK {
		t.Fatalf("the admit route refused an envelope with a flat identity: %d %s", status, body)
	}

	// Every capture route.
	for path, operation := range map[string]string{
		"/capture/v1/hold":    "hold",
		"/capture/v1/seal":    "seal",
		"/capture/v1/publish": "publish",
		"/capture/v1/release": "release",
		"/capture/v1/stat":    "stat",
	} {
		status, body := fixture.call(t, path, hangar.PurposeControlBase, operation,
			identifiedBy(identity(1)))
		if status != http.StatusForbidden {
			t.Errorf("an execution-control warrant was admitted at %s: %d %s", path, status, body)
		}
		if !strings.Contains(string(body), string(hangar.PurposeControlCapture)+" purpose") {
			t.Errorf("the refusal at %s does not name the purpose it wanted: %s", path, body)
		}
	}

	// The mirror: an output-capture warrant at a base route.
	if status, body := fixture.call(t, "/execution/v1/classify",
		hangar.PurposeControlCapture, "classify", identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Errorf("an output-capture warrant was admitted at a base route: %d %s", status, body)
	}
}

// A warrant authorizes one operation.
func TestAReplayedWarrantIsRefusedAndAFreshOneIsNot(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	token := fixture.mint(t, hangar.PurposeControlBase, "classify", identity(1))

	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the first presentation was refused: %d %s", status, body)
	}
	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Errorf("a replayed warrant was admitted: %d %s", status, body)
	}

	// A fresh one still works, so the refusal is about this nonce and not about
	// the daemon having given up.
	if status, body := fixture.call(t, "/execution/v1/classify",
		hangar.PurposeControlBase, "classify", identifiedBy(identity(1))); status != http.StatusOK {
		t.Errorf("a fresh warrant was refused after a replay: %d %s", status, body)
	}
}

// The base surface never requires or synthesizes an output or source field.
//
// This is decision F13's contract obligation stated where it can fail: a base
// caller sends an identity and gets a classification, and nothing on the way
// there or back mentions a hold, a capture or a bucket.
func TestTheBaseSurfaceNeverMentionsTheExtension(t *testing.T) {
	fixture := newRoutes(t)
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
		status, body := fixture.call(t, path, hangar.PurposeControlBase, operation,
			identifiedBy(identity(1)))
		if status != http.StatusOK {
			t.Fatalf("%s answered %d: %s", path, status, body)
		}
		for _, forbidden := range []string{
			"capture", "handoff", "source_hold", "incarnation", "bucket", "scope",
			"digest", "receipt", "writer_ticket",
		} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s answered with %q in it: %s", path, forbidden, body)
			}
		}
	}
}

// The whole capture chain over HTTP: hold, seal, publish, stat, release.
func TestTheCaptureRoutesHoldSealPublishAndRelease(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	status, body := fixture.call(t, "/capture/v1/hold", hangar.PurposeControlCapture, "hold", holdRequest())
	if status != http.StatusOK || !strings.Contains(string(body), `"kind":"hold_acknowledged"`) {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}
	writeFile(t, filepath.Join(fixture.stepDir(), "artifact.txt"), "the bytes")

	// Publishing before the seal is refused: no canonical read begins before
	// the producer has stopped.
	publication := output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: hangar.Digest("sha256:" + string(make64('b'))),
	}
	if status, body := fixture.call(t, "/capture/v1/publish",
		hangar.PurposeControlCapture, "publish", publication); status != http.StatusPreconditionFailed {
		t.Errorf("an unsealed step directory was published: %d %s", status, body)
	}

	fixture.pods.stop(testPod)
	status, body = fixture.sealOverHTTP(t)
	if status != http.StatusOK {
		t.Fatalf("the seal was refused: %d %s", status, body)
	}
	var sealed output.CaptureSealResult
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatalf("decoding the seal: %v", err)
	}
	// The scope is the DERIVED one; the request has no field that could say it.
	if sealed.Scope != fixture.daemon.Namespace().Scope() {
		t.Errorf("the seal answered scope %q", sealed.Scope)
	}

	publication.Digest, publication.Staged = sealed.Digest, sealed.Staged
	status, body = fixture.publishOverHTTP(t, publication)
	if status != http.StatusOK {
		t.Fatalf("the sealed tree was not published: %d %s", status, body)
	}
	var result output.CapturePublishResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if result.Ref.Generation <= 0 || result.Deduplicated || result.Ref.Digest != sealed.Digest {
		t.Errorf("the publish answered %+v", result)
	}

	status, body = fixture.call(t, "/capture/v1/stat", hangar.PurposeControlCapture, "stat",
		output.CaptureStatRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Digest: sealed.Digest})
	if status != http.StatusOK || !strings.Contains(string(body), fmt.Sprint(result.Ref.Generation)) {
		t.Errorf("stat answered %d %s", status, body)
	}

	status, body = fixture.call(t, "/capture/v1/release", hangar.PurposeControlCapture, "release",
		output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput})
	if status != http.StatusOK || !strings.Contains(string(body), output.ReleaseAcknowledged) {
		t.Errorf("release answered %d %s", status, body)
	}
}

// The handshake is the authority on what this daemon speaks. Node labels are
// hints; this is the thing a control plane reads before trusting a statement.
func TestTheHandshakeNamesTheProtocolAndLedger(t *testing.T) {
	fixture := newRoutes(t)

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

// A warrant minted for one execution must not act on another's handoff.
//
// The middleware reads the identity out of the body and checks the warrant
// against it, and five capture routes then act on facts derived from that same
// identity. Three did not: inspect-hold and inspect-seal took the handoff
// straight out of the body, and confirm-seal read its execution out of the
// caller-supplied SealStarted rather than out of the identity the warrant was
// bound to. So execution B, holding nothing, presenting a warrant minted
// for B, read A's hold and A's captured drain set -- and moved A's source to
// `sealed`, which is the state that admits a canonical read.
//
// The control is asserted first, and it is the same three routes answering for
// the execution they belong to.
func TestAWarrantForOneExecutionCannotActOnAnothersCapture(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	// A holds.
	if status, body := fixture.call(t, "/capture/v1/hold", hangar.PurposeControlCapture, "hold",
		holdRequest()); status != http.StatusOK {
		t.Fatalf("A's hold was refused: %d %s", status, body)
	}

	// B is a real, admitted execution on this node. It holds nothing.
	b := executioncontrol.Identity{ExecutionID: "55555555-5555-4555-8555-555555555555", Fence: 1}
	if err := fixture.ledger.Admit(executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion, Identity: b,
		NodeUID: testNode, Capability: "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting B: %v", err)
	}

	// B's warrants, presented with bodies naming A's capture.
	for _, row := range []struct {
		path, operation string
		body            any
	}{
		{"/capture/v1/seal", "seal", sealRequest()},
		{"/capture/v1/release", "release", output.CaptureReleaseRequest{
			ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}},
	} {
		status, body := fixture.callAs(t, b, row.path, hangar.PurposeControlCapture, row.operation, row.body)
		if status != http.StatusForbidden {
			t.Errorf("%s let execution B act on A's capture: %d %s", row.path, status, body)
		}
	}

	// And A's capture is still held.
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Held {
		t.Errorf("A's step directory is %s after B's attempts", class)
	}
}

// A warrant is spent once, and a restart does not forget that.
//
// The replay refusal once lived in the verifier's memory. A daemon restart --
// a crash, a rollout, an OOM kill -- emptied it, so a warrant captured off the
// wire was admitted a second time inside its window, which is up to fifteen
// minutes. The bound was the ledgers' own idempotency rather than the
// warrant, and the auth test said "replayed warrant ... fail closed" without
// the row that a restart is.
//
// The pair: a fresh warrant still works after the restart, so the refusal
// is about this nonce and not about the daemon having given up.
func TestASpentWarrantIsStillSpentAfterARestart(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	token := fixture.mint(t, hangar.PurposeControlBase, "classify", identity(1))
	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusOK {
		t.Fatalf("the first presentation was refused: %d %s", status, body)
	}

	// The record holds the one spent nonce, under its purpose. A nonce that
	// has already expired is planted beside it: it cannot authorize anything,
	// and a record that kept them would grow with every warrant this node
	// ever saw.
	store := fixture.captureFixture.store
	var nonces map[string]time.Time
	if found, err := store.get(controlWarrantRecordName, &nonces); err != nil || !found {
		t.Fatalf("reading the spent record: found=%v err=%v", found, err)
	}
	if len(nonces) != 1 {
		t.Fatalf("the presented warrant was not recorded as spent: %v", nonces)
	}
	var recorded string
	for key := range nonces {
		recorded = key
	}
	if !strings.HasPrefix(recorded, string(hangar.PurposeControlBase)+"|") {
		t.Fatalf("the spent record is keyed %q, not by purpose and nonce", recorded)
	}
	const stale = "execution-control|nonce-from-an-hour-ago"
	nonces[stale] = fixedNow().Add(-time.Hour)
	if err := store.put(controlWarrantRecordName, nonces); err != nil {
		t.Fatalf("planting an expired nonce: %v", err)
	}

	// The restart: a new process over the same control directory.
	fixture.serve(t)

	if status, body := fixture.callWith(t, "/execution/v1/classify", token,
		identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Errorf("a warrant spent before the restart was admitted after it: %d %s", status, body)
	}
	if status, body := fixture.call(t, "/execution/v1/classify",
		hangar.PurposeControlBase, "classify", identifiedBy(identity(1))); status != http.StatusOK {
		t.Errorf("a fresh warrant was refused after the restart: %d %s", status, body)
	}

	var kept map[string]time.Time
	if _, err := store.get(controlWarrantRecordName, &kept); err != nil {
		t.Fatalf("re-reading the spent record: %v", err)
	}
	if _, found := kept[stale]; found {
		t.Error("an expired nonce survived the restart; the record grows without bound")
	}
	if _, found := kept[recorded]; !found {
		t.Error("the spent nonce was pruned along with the expired one")
	}
}

// The recorded start is read over the base surface, under its own operation: a
// warrant minted for classify does not read it, and the answer is the
// stored statement.
func TestTheStartInspectionRouteAnswersWithTheStoredStart(t *testing.T) {
	fixture := newRoutes(t)
	admitted(t, &fixture.ledgerFixture)

	if status, body := fixture.call(t, "/execution/v1/start/inspect", hangar.PurposeControlBase,
		"inspect-start", identifiedBy(identity(1))); status != http.StatusNotFound {
		t.Fatalf("an unstarted execution answered %d: %s", status, body)
	}
	started, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	status, body := fixture.call(t, "/execution/v1/start/inspect", hangar.PurposeControlBase,
		"inspect-start", identifiedBy(identity(1)))
	if status != http.StatusOK {
		t.Fatalf("the start inspection answered %d: %s", status, body)
	}
	var read executioncontrol.Acknowledgement
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !sameStatement(read, started) {
		t.Fatalf("the route answered a different statement: %s", body)
	}
	if status, body := fixture.call(t, "/execution/v1/start/inspect", hangar.PurposeControlBase,
		"classify", identifiedBy(identity(1))); status != http.StatusForbidden {
		t.Fatalf("a classify warrant read the start: %d %s", status, body)
	}
}

// A publish that takes longer than the caller's HTTP timeout -- here because
// the one scratch slot is busy for longer than that -- is not a failed call:
// the route answers 202 at once, the upload runs on the node, and the next
// ask after it finishes answers the generation.
func TestAPublishSlowerThanTheClientTimeoutIsPolledToItsGeneration(t *testing.T) {
	fixture := newRoutes(t)
	fixture.capture.sealWait = time.Minute
	admitted(t, &fixture.ledgerFixture)
	if status, body := fixture.call(t, "/capture/v1/hold", hangar.PurposeControlCapture, "hold", holdRequest()); status != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}
	writeFile(t, filepath.Join(fixture.stepDir(), "artifact.txt"), "the bytes")
	fixture.pods.stop(testPod)
	status, body := fixture.sealOverHTTP(t)
	if status != http.StatusOK {
		t.Fatalf("sealing: %d %s", status, body)
	}
	var sealed output.CaptureSealResult
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatal(err)
	}

	// Another tree is spooling, and holds the only scratch slot for longer
	// than the client will wait.
	busy, err := fixture.api.spooling(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(2500 * time.Millisecond)
		busy()
		close(released)
	}()
	fixture.client = &http.Client{Timeout: time.Second}

	publication := output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: sealed.Digest,
	}
	started := time.Now()
	status, body = fixture.call(t, "/capture/v1/publish", hangar.PurposeControlCapture, "publish", publication)
	if status != http.StatusAccepted || !strings.Contains(string(body), `"publishing"`) {
		t.Fatalf("the first publish answered %d %s, want 202 publishing", status, body)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Errorf("the first publish took %s; it should only start the upload", time.Since(started))
	}
	// Concurrent polls see the one job.
	status, _ = fixture.call(t, "/capture/v1/publish", hangar.PurposeControlCapture, "publish", publication)
	if status != http.StatusAccepted {
		t.Fatalf("a poll while the slot is busy answered %d", status)
	}

	status, body = fixture.publishOverHTTP(t, publication)
	<-released
	if status != http.StatusOK {
		t.Fatalf("the polled publish answered %d %s", status, body)
	}
	var result output.CapturePublishResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Ref.Digest != sealed.Digest || result.Ref.Generation <= 0 {
		t.Fatalf("the publish answered %+v for %s", result.Ref, sealed.Digest)
	}
	if time.Since(started) < 2*time.Second {
		t.Errorf("the publish finished in %s, before the slot was free", time.Since(started))
	}
	if keys := listKeys(t, fixture.objects, fixture.bucket); len(keys) != 1 {
		t.Errorf("the polled publish left %d objects: %v", len(keys), keys)
	}
}
