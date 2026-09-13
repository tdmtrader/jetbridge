package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Nothing this daemon emits may name where anything is.
//
// Reqs 3, 7 and 12 say the control API takes identities and never a path, a
// hostPath, a bucket, a scope or an object key, and that the server derives
// every location. That is a rule about REQUESTS, and the route table already
// enforces it. This is the other half, which nothing enforced: a daemon that
// refuses to accept an object key and then prints one in its refusal has told
// the caller the thing the rule exists to withhold.
//
// The plan keeps this in Go on purpose -- "a log line is not a brine
// observable, and the scan is a source/output grep" -- so it is exactly that:
// drive the real routes over real HTTP, collect every response body and
// everything the process wrote to stderr and to the standard logger, and grep
// for the values this test knows and the caller must not learn.
//
// Capabilities are in the forbidden set for a different reason than the paths.
// A path in a refusal tells a caller where to go looking; a capability in one
// tells whoever reads the log how to be that caller.

// forbidden is one value that must not appear, with what it would give away.
type forbidden struct {
	value string
	what  string
}

func (fixture *routeFixture) forbiddenValues(t *testing.T, extra ...forbidden) []forbidden {
	t.Helper()

	values := []forbidden{
		{fixture.dir, "the daemon's control directory on this node"},
		{filepath.Join(fixture.dir, "steps"), "the managed steps root (the hostPath)"},
		{fixture.bucket, "the output bucket"},
		{fixture.config.OutputPrefix, "the output bucket's prefix"},
		{fixture.config.ScratchDir, "the daemon's scratch directory"},
		{fixture.config.ReceiptKeyFile, "the receipt signing key's location"},
		{fixture.config.ControlKeyFile, "the control signing key's location"},
	}
	for _, key := range listKeys(t, fixture.store, fixture.bucket) {
		values = append(values, forbidden{key, "a published object's key"})
	}
	for _, token := range fixture.minted {
		values = append(values, forbidden{string(token), "a control capability"})
	}

	return append(values, extra...)
}

// capturedStderr redirects the process's stderr and the standard logger for the
// duration of the test, and returns everything written.
//
// The output daemon writes no log lines today -- there is a source-derived
// assertion below that says so and fails when that stops being true -- so this
// exists to catch the FIRST one somebody adds, on the day they add it, rather
// than to walk a stream that is currently empty.
func capturedStderr(t *testing.T) func() string {
	t.Helper()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening the capture pipe: %v", err)
	}
	realStderr, realLog := os.Stderr, log.Writer()
	os.Stderr = write
	log.SetOutput(write)

	collected := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(read)
		collected <- string(body)
	}()

	var once bool
	return func() string {
		if once {
			return ""
		}
		once = true
		os.Stderr, _ = realStderr, realStderr
		log.SetOutput(realLog)
		_ = write.Close()
		text := <-collected
		_ = read.Close()

		return text
	}
}

func TestNothingTheDaemonEmitsNamesAPathBucketObjectKeyOrCapability(t *testing.T) {
	stderr := capturedStderr(t)
	defer func() {
		if text := stderr(); text != "" {
			t.Logf("the daemon wrote to stderr: %s", text)
		}
	}()

	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	// ---- reserve, then hold ----
	//
	// The reservation is on the scan too: it is the one route whose ANSWER is a
	// directory, so a daemon that leaked its own storage root would leak it
	// here first.
	held := fixture.reserveOverHTTP(t, identity(1), admission())

	status, body := fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", held)
	if status != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}
	var hold output.CaptureAcknowledgement
	if err := json.Unmarshal(body, &hold); err != nil {
		t.Fatalf("decoding the hold: %v", err)
	}

	// A hold repeated with a different fence: a typed conflict, and the first
	// refusal on the list.
	conflicting := held
	conflicting.SourceLeaseID = "99999999-9999-4999-8999-999999999999"
	fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", conflicting)

	// And a hold naming an incarnation the daemon never reserved: the refusal
	// names two directories, and neither may carry the daemon's storage root.
	foreign := held
	foreign.Incarnation.HandleGeneration += 100
	fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", foreign)

	// A hold naming a PATH. The refusal for this one is the most tempting place
	// in the whole surface to echo what the caller sent.
	fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", map[string]any{
		"protocol_version": output.ProtocolVersion,
		"execution":        identity(1),
		"activation_epoch": fixture.epoch,
		"handoff_id":       testHandoff,
		"source_lease_id":  testLease,
		"output":           testOutput,
		"source_path":      filepath.Join(fixture.dir, "steps", "somewhere-i-chose"),
	})

	root, err := fixture.source.ResolveIncarnation(hold.Incarnation)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.txt"), []byte("the bytes"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// ---- writer ticket ----
	ticket := output.WriterAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		Incarnation:     hold.Incarnation,
		WriterTicketID:  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		WriterFence:     1,
		PodUID:          testPod,
	}
	fixture.call(t, "/capture/v1/writer-ticket", output.CaptureFacet, "issue-writer-ticket", ticket)
	fixture.call(t, "/capture/v1/writer-ticket/close", output.CaptureFacet,
		"close-writer-ticket", ticket)

	// ---- seal ----
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

	// A ticket issued AFTER the seal: the refusal that names the incarnation.
	fixture.call(t, "/capture/v1/writer-ticket", output.CaptureFacet, "issue-writer-ticket", ticket)

	started, err := fixture.source.InspectSeal(testHandoff, identity(1))
	if err != nil {
		t.Fatalf("inspecting the seal: %v", err)
	}
	if _, err := fixture.source.ConfirmSeal(t.Context(), output.SealConfirmation{
		Started: started, CaptureFence: captureFence, ObservedAt: output.NewTimestamp(fixedNow()),
	}); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	// ---- publish ----
	publication := output.PublicationRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		ReservationID:   "44444444-4444-4444-8444-444444444444",
		CaptureFence:    captureFence,
	}
	status, body = fixture.call(t, "/capture/v1/publish",
		output.CaptureFacet, "publish", publication)
	if status != http.StatusOK {
		t.Fatalf("publishing: %d %s", status, body)
	}
	var published output.PublicationResult
	if err := json.Unmarshal(body, &published); err != nil {
		t.Fatalf("decoding the publication: %v", err)
	}

	// The same publication again: the dedup path, which reads the object back
	// and classifies what it finds -- and does so BY KEY.
	fixture.call(t, "/capture/v1/publish", output.CaptureFacet, "publish", publication)

	// A publication naming a bucket, a scope and a key the caller chose. If any
	// refusal echoes its input this is the one.
	for _, chosen := range []output.CallerNamespaceRequest{
		{Bucket: "somebody-elses-bucket"},
		{Scope: "o0000000000000000000000000000000000000000"},
		{Key: "hangar/v1/scopes/x/trees/sha256/dead.tar.zst"},
		{Prefix: "deployments/red"},
	} {
		carrying := publication
		carrying.Namespace = chosen
		fixture.call(t, "/capture/v1/publish", output.CaptureFacet, "publish", carrying)
	}

	// A publication for a reservation the store has never heard of, so the
	// object-store read misses and the miss is translated.
	missing := publication
	missing.ReservationID = "55555555-5555-4555-8555-555555555555"
	fixture.call(t, "/capture/v1/publish", output.CaptureFacet, "publish", missing)

	// ---- stat ----
	//
	// Two challenges. The first names the object that was just published, so
	// the daemon reads the store BY KEY and answers. The second names a digest
	// nothing was ever stored under, so the read MISSES -- and a miss is where
	// an object-store error is translated, which is the single most likely
	// place in this daemon for a key to escape into a message.
	challenge := func(digest hangar.Digest) map[string]any {
		return map[string]any{
			"execution": identity(1),
			"challenge": output.StatChallenge{
				Nonce:           "a-stat-challenge-nonce",
				HandoffID:       testHandoff,
				ActivationEpoch: fixture.epoch,
				ReservationID:   "44444444-4444-4444-8444-444444444444",
				Ref: hangar.TreeRef{
					Scope:      fixture.daemon.Namespace().Scope(),
					Digest:     digest,
					Generation: published.Ref.Generation,
				},
				CaptureFence: captureFence,
				IssuedAt:     output.NewTimestamp(fixedNow()),
				NotAfter:     output.NewTimestamp(fixedNow().Add(time.Minute)),
			},
			"claims": output.ReceiptClaims{
				Execution:            identity(1),
				ProducerCheckpointID: "opaque-checkpoint",
				Incarnation:          hold.Incarnation,
				Output:               testOutput,
				WriterFence:          1,
			},
		}
	}
	fixture.call(t, "/capture/v1/stat", output.CaptureFacet, "stat",
		challenge(published.Ref.Digest))
	fixture.call(t, "/capture/v1/stat", output.CaptureFacet, "stat",
		challenge(hangar.Digest("sha256:"+strings.Repeat("de", 32))))

	// ---- release ----
	intent := output.ReleaseIntent{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionCapture,
		Execution:       identity(1),
		ActivationEpoch: fixture.epoch,
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		ReleaseIntentID: "66666666-6666-4666-8666-666666666666",
		Incarnation:     hold.Incarnation,
	}
	fixture.call(t, "/capture/v1/release", output.CaptureFacet, "release-hold", intent)

	// A second, DIFFERENT release intent: the conflict that names both.
	second := intent
	second.ReleaseIntentID = "77777777-7777-4777-8777-777777777777"
	fixture.call(t, "/capture/v1/release", output.CaptureFacet, "release-hold", second)

	// ---- the authorization refusals, where a token could be echoed ----
	fixture.call(t, "/capture/v1/hold", executioncontrol.BaseFacet, "hold", held)
	fixture.callWith(t, "/capture/v1/hold", "a-forged-capability", held)
	if len(fixture.minted) > 0 {
		fixture.callWith(t, "/capture/v1/seal", fixture.minted[0], admission())
	}

	// ---- the scan ----
	emitted := append([]string{}, fixture.emitted...)
	if text := stderr(); text != "" {
		emitted = append(emitted, "stderr -> "+text)
	}
	if len(emitted) < 15 {
		t.Fatalf("only %d answers were collected; this scan cannot fail on so few", len(emitted))
	}

	for _, secret := range fixture.forbiddenValues(t) {
		if secret.value == "" {
			continue
		}
		for _, line := range emitted {
			if strings.Contains(line, secret.value) {
				t.Errorf("the daemon emitted %s (%q):\n    %s",
					secret.what, secret.value, line)
			}
		}
	}
}

// forEachProductionFile hands each non-test .go file in dir to visit.
func forEachProductionFile(t *testing.T, dir string, visit func(name, source string)) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	seen := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		seen++
		visit(name, string(source))
	}
	if seen == 0 {
		t.Fatal("scanned no production files -- this guard cannot fail")
	}
}

// The log surface is empty, and this is what says so.
//
// The scan above walks stderr and the standard logger, and finds nothing there
// because this daemon does not log. That makes the log half of the scan
// vacuous today, and a vacuous assertion that quietly becomes non-vacuous is
// the worst kind: somebody adds a logger, it starts printing incarnation paths,
// and the scan that "covers logs" was never extended to see it.
//
// So the emptiness is pinned. When a logger arrives, this fails, and whoever
// adds it has to route it through the scan.
func TestTheOutputDaemonWritesNoLogLineOutsideItsStartupBanner(t *testing.T) {
	// main.go is the one exception, and it is worth naming precisely rather
	// than waving at.
	//
	// It prints a startup banner -- bucket, prefix, derived scope, ledger path,
	// key file paths -- and a fatal error, both to an io destination main()
	// passes in, once, before the listener accepts anything. Those values are
	// this daemon restating its OWN flags, which are already in its Pod spec
	// and readable by anyone who can read the Pod at all; withholding them from
	// the operator's `kubectl logs` while leaving them in `kubectl get pod -o
	// yaml` would protect nobody. What Reqs 3, 7 and 12 forbid is telling a
	// CALLER where things are, and no caller can reach a line printed before
	// the first request.
	//
	// Every other file must stay silent, so that "the daemon's log lines" and
	// "the daemon's HTTP answers" remain the same set -- the set the scan above
	// walks.
	const startupOnly = "main.go"

	writers := map[string][]string{}
	forEachProductionFile(t, ".", func(name, source string) {
		if name == startupOnly {
			return
		}
		for _, emitter := range []string{
			"log.Print", "log.Fatal", "log.Panic", "slog.", "println(",
			"fmt.Println", "fmt.Print(", "fmt.Fprint", "os.Stdout", "os.Stderr",
		} {
			if strings.Contains(source, emitter) {
				writers[name] = append(writers[name], emitter)
			}
		}
	})

	for name, found := range writers {
		t.Errorf("%s writes to a log or to stdout/stderr: %v\n"+
			"    Every line this daemon emits after startup is on a request path and must be "+
			"walked by TestNothingTheDaemonEmitsNamesAPathBucketObjectKeyOrCapability, which "+
			"greps it for source paths, buckets, object keys and capabilities. Add it to that "+
			"scan's collection, then list it here with the reason it is safe.", name, found)
	}

	// And the banner really is confined to startup: run() takes its destination
	// as a parameter, so nothing below it can print to the process's own
	// streams without this guard seeing it.
	banner, err := os.ReadFile(startupOnly)
	if err != nil {
		t.Fatalf("reading %s: %v", startupOnly, err)
	}
	if !strings.Contains(string(banner), "func run(ctx context.Context, config Config, out *os.File) error") {
		t.Error("run() no longer takes its output destination as a parameter. The startup " +
			"banner's confinement to startup was that signature; check where it prints now.")
	}
}
