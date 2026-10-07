package outputplane

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

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

	// ---- hold ----
	held := holdRequest()
	status, body := fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", held)
	if status != http.StatusOK {
		t.Fatalf("the hold was refused: %d %s", status, body)
	}

	// A hold from another Pod: a typed conflict naming the capture.
	conflicting := held
	conflicting.PodUID = "pod-recreated"
	fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", conflicting)

	// A hold naming a PATH. The refusal for this one is the most tempting place
	// in the whole surface to echo what the caller sent.
	fixture.call(t, "/capture/v1/hold", output.CaptureFacet, "hold", map[string]any{
		"protocol_version": output.ProtocolVersion,
		"execution":        identity(1),
		"output":           testOutput,
		"pod_uid":          testPod,
		"source_path":      filepath.Join(fixture.dir, "steps", "somewhere-i-chose"),
	})

	writeFile(t, filepath.Join(fixture.stepDir(), "artifact.txt"), "the bytes")

	// ---- seal: first while the Pod still runs, then after it stops ----
	fixture.call(t, "/capture/v1/seal", output.CaptureFacet, "seal", sealRequest())
	fixture.pods.stop(testPod)
	status, body = fixture.sealOverHTTP(t)
	if status != http.StatusOK {
		t.Fatalf("sealing: %d %s", status, body)
	}
	var sealed output.CaptureSealResult
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatalf("decoding the seal: %v", err)
	}

	// ---- publish ----
	publication := output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: sealed.Digest, Staged: sealed.Staged,
	}
	status, body = fixture.call(t, "/capture/v1/publish", output.CaptureFacet, "publish", publication)
	if status != http.StatusOK {
		t.Fatalf("publishing: %d %s", status, body)
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

	// A publication for a digest the sealed tree is not: the refusal compares
	// two digests and must name no location.
	wrong := publication
	wrong.Digest, wrong.Staged = hangar.Digest("sha256:"+strings.Repeat("ab", 32)), ""
	fixture.call(t, "/capture/v1/publish", output.CaptureFacet, "publish", wrong)

	// ---- stat: one present, one absent ----
	//
	// The absent one MISSES in the store, and a miss is where an object-store
	// error is translated -- the single most likely place in this daemon for a
	// key to escape into a message.
	for _, digest := range []hangar.Digest{sealed.Digest, hangar.Digest("sha256:" + strings.Repeat("de", 32))} {
		fixture.call(t, "/capture/v1/stat", output.CaptureFacet, "stat", output.CaptureStatRequest{
			ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Digest: digest,
		})
	}

	// ---- release, twice ----
	release := output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}
	fixture.call(t, "/capture/v1/release", output.CaptureFacet, "release", release)
	fixture.call(t, "/capture/v1/release", output.CaptureFacet, "release", release)

	// A seal of the released capture: the refusal names the missing marker.
	fixture.call(t, "/capture/v1/seal", output.CaptureFacet, "seal", sealRequest())

	// ---- the authorization refusals, where a token could be echoed ----
	fixture.call(t, "/capture/v1/hold", executioncontrol.BaseFacet, "hold", held)
	fixture.callWith(t, "/capture/v1/hold", "a-forged-capability", held)
	if len(fixture.minted) > 0 {
		fixture.callWith(t, "/capture/v1/seal", fixture.minted[0], sealRequest())
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
	// plane.go is the one exception, and it is worth naming precisely rather
	// than waving at.
	//
	// It prints a startup banner -- bucket, prefix, derived scope, ledger path,
	// key ids -- to an io destination the artifact daemon's main() passes in,
	// once, before the listener accepts anything. Those values are
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
	const startupOnly = "plane.go"

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

	// And the banner really is confined to startup: Open() takes its destination
	// as a parameter, so nothing below it can print to the process's own
	// streams without this guard seeing it.
	banner, err := os.ReadFile(startupOnly)
	if err != nil {
		t.Fatalf("reading %s: %v", startupOnly, err)
	}
	if !strings.Contains(string(banner), "func Open(ctx context.Context, config Config, nodes kubernetes.Interface, daemonCertificate []byte, out io.Writer) (_ *Plane, err error)") {
		t.Error("Open() no longer takes its output destination as a parameter. The startup " +
			"banner's confinement to startup was that signature; check where it prints now.")
	}
}

// Which sentinels reach 503, and that 503 is the DAEMON's fault and not the
// caller's.
//
// This is the output half of branch-review finding F4. The artifact daemon
// already draws the line: `artifact_daemon_refusals_total` answers "how often
// did the daemon turn a client away for something the CLIENT did", and a bucket
// that will not answer, a cancelled context and an unclassified store error are
// none of those -- so they go through `hangarUnavailable`, which logs and does
// not count, and `refusal_visibility_test.go`'s `known` map enumerates it
// deliberately.
//
// This daemon has no refusal counter yet, so there is nothing to split. What
// there is, and what this pins, is the CLASSIFICATION the counter will be built
// on: these sentinels are server-side faults, and a counter that included them
// would make one metric mean two things exactly as the artifact daemon's did.
// The day this daemon gains one, this table is what it has to be consistent
// with, and changing it is a deliberate edit rather than a silent drift.
func TestTheDaemonsOwnFaultsAreNotCallerRefusals(t *testing.T) {
	// The control first: a caller's own mistakes, which a refusal counter is
	// exactly the metric for. Without these, "server faults are 503" would pass
	// on a daemon that answered 503 to everything.
	for _, row := range []struct {
		err    error
		status int
		what   string
	}{
		{output.ErrUnauthorized, http.StatusForbidden, "a capability that does not authorize this"},
		{output.ErrNotFound, http.StatusNotFound, "an identity this node does not know"},
		{output.ErrConflict, http.StatusConflict, "a fact that disagrees with a durable one"},
		{output.ErrSealed, http.StatusConflict, "a write over a sealed source"},
		{output.ErrIncomplete, http.StatusBadRequest, "a request missing a required fact"},
		{output.ErrInvalidIdentity, http.StatusBadRequest, "a malformed identity"},
		{output.ErrSealUnconfirmed, http.StatusPreconditionFailed, "an unproved drain"},
	} {
		recorder := httptest.NewRecorder()
		writeError(recorder, row.err)
		if recorder.Code != row.status {
			t.Errorf("%s answered %d, not %d", row.what, recorder.Code, row.status)
		}
		if recorder.Code == http.StatusServiceUnavailable {
			t.Errorf("%s was classified as a daemon fault; it is the caller's", row.what)
		}
	}

	// And the daemon's own, which a refusal counter must never include.
	for _, row := range []struct {
		err  error
		what string
	}{
		{output.ErrInfrastructure, "a store that will not answer"},
		{output.ErrCorrupt, "a record this node cannot read"},
	} {
		recorder := httptest.NewRecorder()
		writeError(recorder, row.err)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s answered %d; a server-side fault reported as a client error is how a "+
				"bucket outage gets read as bad pipelines", row.what, recorder.Code)
		}
	}
}
