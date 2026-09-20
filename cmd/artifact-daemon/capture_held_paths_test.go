package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/concourse/concourse/artifactwire"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/hangar/output/ledger"
)

// Every destructive path in this daemon, against a source a capture holds.
//
// `DELETE /artifacts/` was the one call site wired when the classifier landed,
// and it was wired against the one caller that names the incarnation exactly.
// Every other destructive path here -- the sweeper on its timer, the stream-in
// replacement, the ordinary PUT, a resolve writing into a destination, an alias
// remapped or reused -- reaches the same bytes and asked nobody.
//
// Each case below is a PAIR. The held source must survive, and an unheld one
// must not: a guard that refuses everything is not a guard, it is an outage,
// and it passes exactly the half of this test that a real guard passes.

const (
	heldExecution  = "77777777-7777-4777-8777-777777777777"
	heldGeneration = 9
	heldOutput     = "result"
)

// heldIncarnation is what the ledger names.
func heldIncarnation() string {
	return fmt.Sprintf("%s.%d/%s", heldExecution, heldGeneration, heldOutput)
}

// heldStepDir is what the Reaper and the sweeper name: the PARENT.
func heldStepDir() string {
	return fmt.Sprintf("%s.%d", heldExecution, heldGeneration)
}

// capturedNode builds a storage root holding one capture-held source, one
// unheld step directory, and the control record that says so.
func capturedNode(t *testing.T) string {
	t.Helper()

	storage := t.TempDir()
	for _, dir := range []string{
		filepath.Join("steps", heldIncarnation()),
		filepath.Join("steps", "unheld-handle", "out"),
	} {
		if err := os.MkdirAll(filepath.Join(storage, dir), 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	// The producer's bytes. An empty source makes "it survived" indistinguishable
	// from "it was recreated".
	for _, file := range []string{
		filepath.Join("steps", heldIncarnation(), "artifact.txt"),
		filepath.Join("steps", "unheld-handle", "out", "artifact.txt"),
	} {
		if err := os.WriteFile(filepath.Join(storage, file), []byte("the producer wrote this"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", file, err)
		}
	}

	control := filepath.Join(storage, ledger.ControlDirName)
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatalf("creating the control directory: %v", err)
	}
	record, err := json.Marshal(map[string]any{
		"record_version": "hangar-output-control-record-v1",
		"checksum":       "not-read-by-the-classifier",
		"body": map[string]any{
			"state": "held",
			"incarnation": map[string]any{
				"execution_id":      heldExecution,
				"handle_generation": heldGeneration,
				"output":            heldOutput,
			},
		},
	})
	if err != nil {
		t.Fatalf("encoding the hold: %v", err)
	}
	if err := os.WriteFile(filepath.Join(control, "source-88888888-8888-4888-8888-888888888888.json"),
		record, 0o600); err != nil {
		t.Fatalf("writing the hold: %v", err)
	}

	return storage
}

func capturedServer(t *testing.T) (*Server, string) {
	t.Helper()

	storage := capturedNode(t)
	server, err := NewServer(lagertest.NewTestLogger("capture-held"), storage, "node-1")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	return server, storage
}

func stillThere(t *testing.T, storage, relative string) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(storage, "steps", relative, "artifact.txt"))
	if err != nil {
		t.Fatalf("the producer's bytes under %s are gone: %v", relative, err)
	}
	if string(body) != "the producer wrote this" {
		t.Fatalf("the producer's bytes under %s were replaced: %q", relative, body)
	}
}

// The Reaper deletes the STEP DIRECTORY, not the incarnation.
//
// `cleanupDaemonSetArtifacts` issues `DELETE /artifacts/steps/<handle>`. The
// hold names `<execution>.<generation>/<output>` beneath that handle. So the
// one call site that was wired was wired against a path shape no production
// caller sends, and the caller that does send one was answered 204.
func TestTheReaperFacingDeleteOfAStepDirectoryContainingAHeldSourceIsRefused(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	remove := func(key string) int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/artifacts/"+key, nil))

		return recorder.Code
	}

	// The control first, and it is the same SHAPE as the refused call: a step
	// directory, deleted by handle, with an artifact inside it.
	if code := remove("steps/unheld-handle"); code != http.StatusNoContent {
		t.Fatalf("an unheld step directory answered %d", code)
	}

	if code := remove("steps/" + heldStepDir()); code != http.StatusConflict {
		t.Errorf("the Reaper's delete of a step directory containing a held source answered %d", code)
	}
	stillThere(t, storage, heldIncarnation())
}

// The sweeper removes expired step directories on a timer.
//
// Nothing refreshes a held source's mtime: the producer wrote it and exited,
// and the capture is waiting for a seal. So the sweeper is the destructive path
// a held source is MOST likely to meet, and it asked nobody.
func TestTheSweeperSparesACaptureHeldStepDirectoryAndStillSweepsAnExpiredOne(t *testing.T) {
	server, storage := capturedServer(t)

	stale := time.Now().Add(-2 * time.Hour)
	for _, dir := range []string{heldStepDir(), "unheld-handle"} {
		if err := os.Chtimes(filepath.Join(storage, "steps", dir), stale, stale); err != nil {
			t.Fatalf("ageing %s: %v", dir, err)
		}
	}

	sweeper := NewSweeper(lagertest.NewTestLogger("sweep"), storage, time.Hour, time.Hour, server.registry)
	sweeper.SetGuard(server.guard)
	sweeper.SetSourceLedger(server.sourceLedger)
	sweeper.SweepOnce()

	// The control: an expired, unheld step directory is swept. If it survives,
	// the guard has turned the sweeper off rather than taught it to ask.
	if _, err := os.Stat(filepath.Join(storage, "steps", "unheld-handle")); err == nil {
		t.Error("the sweeper spared an expired step directory nothing held")
	}

	stillThere(t, storage, heldIncarnation())
}

// tarOf builds a one-entry tar, so a stream-in has something to replace with.
func oneEntryTar(t *testing.T, name, content string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	return buffer.Bytes()
}

// Stream-in REPLACES: it removes whatever is at the key and renames a freshly
// extracted tree over it. Pointed at a held source that is the whole damage in
// one call, and it leaves a tree that looks perfectly healthy.
func TestStreamInWillNotReplaceACaptureHeldSourceAndStillReplacesAnUnheldOne(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	streamIn := func(key string) int {
		request := httptest.NewRequest(http.MethodPut, "/stream-in/"+key,
			bytes.NewReader(oneEntryTar(t, "artifact.txt", "somebody else's bytes")))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	if code := streamIn("unheld-handle/out"); code != http.StatusCreated {
		t.Fatalf("an unheld stream-in answered %d", code)
	}

	for _, key := range []string{heldIncarnation(), heldStepDir()} {
		if code := streamIn(key); code != http.StatusConflict {
			t.Errorf("a stream-in over %s answered %d", key, code)
		}
	}
	stillThere(t, storage, heldIncarnation())
}

// The ordinary PUT renames a temp file over its key. Under a held source that
// is a replacement of the producer's bytes with no ticket and no record.
func TestPutWillNotWriteIntoACaptureHeldSourceAndStillWritesElsewhere(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	put := func(key string) int {
		request := httptest.NewRequest(http.MethodPut, "/artifacts/"+key,
			strings.NewReader("somebody else's bytes"))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	if code := put("steps/unheld-handle/out/artifact.txt"); code != http.StatusCreated {
		t.Fatalf("an unheld PUT answered %d", code)
	}

	if code := put("steps/" + heldIncarnation() + "/artifact.txt"); code != http.StatusConflict {
		t.Errorf("a PUT into a capture-held source answered %d", code)
	}
	stillThere(t, storage, heldIncarnation())
}

// Resolve copies an artifact INTO a caller-named destination, clearing whatever
// is there first. The destination is caller-supplied, contained in the store,
// and nothing stopped it naming a held source.
func TestResolveWillNotClearACaptureHeldDestinationAndStillClearsAnUnheldOne(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	if _, err := server.registry.Register("some-artifact",
		filepath.Join(storage, "steps", "unheld-handle", "out")); err != nil {
		t.Fatalf("registering the source: %v", err)
	}

	resolve := func(dest string) artifactwire.ResolveResponse {
		body, err := json.Marshal(artifactwire.ResolveRequest{Key: "some-artifact", Dest: dest})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		var answer artifactwire.ResolveResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			// A structural 400 is plain text. Carry it through as the error so
			// the assertion below reads the same either way.
			return artifactwire.ResolveResponse{Status: "error", Error: recorder.Body.String()}
		}

		return answer
	}

	// A resolve destination is a VOLUME inside a steps entry, which is exactly
	// the shape a source incarnation has: `steps/<handle>/<output>`.
	if answer := resolve(filepath.Join(storage, "steps", "unheld-handle", "fresh")); answer.Status != "ok" {
		t.Fatalf("an unheld resolve answered %+v", answer)
	}

	answer := resolve(filepath.Join(storage, "steps", heldIncarnation()))
	if answer.Status == "ok" {
		t.Errorf("a resolve into the held source succeeded")
	}
	if !strings.Contains(answer.Error, "capture") {
		t.Errorf("the refusal does not say why: %q", answer.Error)
	}
	stillThere(t, storage, heldIncarnation())
}

// Alias REUSE: a second key pointed at a held source hands another consumer a
// name for bytes a capture is about to seal, and every destructive path that
// takes a key can then reach it under the new one.
//
// Alias REMAP: a key that names a held source, pointed somewhere else, does not
// destroy the bytes -- it destroys the only way anything finds them. Req 3
// names remap and reuse beside cleanup for that reason.
func TestAnAliasIsNeitherReusedForNorRemappedOffACaptureHeldSource(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	register := func(key, localPath string) int {
		body, err := json.Marshal(artifactwire.RegisterRequest{Key: key, LocalPath: localPath})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	unheld := filepath.Join(storage, "steps", "unheld-handle", "out")
	held := filepath.Join(storage, "steps", heldIncarnation())

	// The control: an ordinary alias onto an unheld path is registered.
	if code := register("ordinary-alias", unheld); code != http.StatusCreated {
		t.Fatalf("an ordinary alias answered %d", code)
	}

	// Reuse.
	if code := register("a-second-name", held); code != http.StatusConflict {
		t.Errorf("a second alias onto a capture-held source answered %d", code)
	}

	// Remap. A remap starts from a mapping that was legitimate when it was
	// made, so the alias is placed while the hold is momentarily off the disk
	// -- which is the real sequence: the ATC registers the volume, and the
	// capture holds it afterwards.
	record := filepath.Join(storage, ledger.ControlDirName,
		"source-88888888-8888-4888-8888-888888888888.json")
	held0, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading the hold: %v", err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatalf("lifting the hold: %v", err)
	}
	if _, err := server.registry.RegisterAlias("the-captures-name", held); err != nil {
		t.Fatalf("seeding the mapping: %v", err)
	}
	if err := os.WriteFile(record, held0, 0o600); err != nil {
		t.Fatalf("restoring the hold: %v", err)
	}
	if code := register("the-captures-name", unheld); code != http.StatusConflict {
		t.Errorf("remapping a capture-held source's alias elsewhere answered %d", code)
	}
	if rel, ok := server.registry.Lookup("the-captures-name"); !ok ||
		!strings.HasSuffix(string(rel), heldIncarnation()) {
		t.Errorf("the capture's alias now points at %q", rel)
	}
}

// The ordinary daemon's API must never reach the output plane's LEDGER.
//
// Every case above is about the source a capture holds. This one is about the
// thing that says a source is held. `refuseIfCaptureHeld` strips `steps/` and
// answers `Unmanaged` for everything outside it, so the control directory was
// not a location the guard protected -- it was a location the daemon SERVED:
// `DELETE /artifacts/.hangar-output-control/source-….json` answered 204 and the
// hold was gone, after which the very next delete of the source it protected
// answered 204 too. The whole classifier is downstream of a file this API could
// remove, and `control_store.go`'s "a ledger the Sweeper could delete is a
// ledger that fails open" is exactly as true of `DELETE /artifacts/`.
//
// The pair: every route refuses the control directory, and the hold it holds
// still refuses the destructive call it exists to refuse -- afterwards, from
// the same server, so a guard that broke the classifier fails here too.
func TestTheOutputLedgersControlDirectoryIsNotReachableThroughTheOrdinaryAPI(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	recordKey := ledger.ControlDirName + "/source-88888888-8888-4888-8888-888888888888.json"
	recordPath := filepath.Join(storage, filepath.FromSlash(recordKey))
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("reading the hold: %v", err)
	}

	// The control, first: while the record is there the held source is refused.
	// If this line ever fails, nothing below means anything.
	do := func(method, target string, body []byte) int {
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader(body)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, target, reader))

		return recorder.Code
	}

	if code := do(http.MethodDelete, "/artifacts/steps/"+heldStepDir(), nil); code != http.StatusConflict {
		t.Fatalf("the held source answered %d before the ledger was touched", code)
	}

	// A resolve DESTINATION and a register LOCAL PATH are the two absolute
	// paths a caller chooses. Both land inside the store and both clear what is
	// there, so both are asserted BEFORE the key routes below -- a resolve of a
	// key nothing registered, or a register of a path a previous row deleted,
	// would be refused for a reason that has nothing to do with the ledger.
	if _, err := server.registry.Register("some-artifact",
		filepath.Join(storage, "steps", "unheld-handle", "out")); err != nil {
		t.Fatalf("registering the source: %v", err)
	}
	dest, err := json.Marshal(artifactwire.ResolveRequest{
		Key: "some-artifact", Dest: filepath.Join(storage, ledger.ControlDirName, "quarantine"),
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if code := do(http.MethodPost, "/resolve", dest); code == http.StatusOK {
		t.Error("a resolve destination inside the control directory was accepted")
	}
	registration, err := json.Marshal(artifactwire.RegisterRequest{Key: "the-ledger", LocalPath: recordPath})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if code := do(http.MethodPost, "/register", registration); code == http.StatusCreated {
		t.Error("an alias onto the output ledger's own record was registered")
	}

	// A PLANTED SYMLINK, which is the vector the key door cannot see.
	//
	// `validateRequestKey` reads the name a request chose, and
	// "steps/unheld-handle/out/link/source-….json" is a lexically clean key. It
	// is `containedRelKey`, through `locateArtifact`, that resolves the path and
	// sees where it lands. The link is WITHIN the root and relative, which is
	// exactly what a producer writing on the hostPath can plant -- tar
	// extraction refuses such a target at `validateSymlinkTarget`, so the only
	// way one exists is that something wrote it directly.
	//
	// With the refusal removed from `containedRelKey` alone, the PUT below
	// rewrites the hold record through the link and every route above stays
	// green: the two doors answer different questions and neither subsumes the
	// other.
	link := filepath.Join(storage, "steps", "unheld-handle", "out", "link")
	if err := os.Symlink(filepath.Join("..", "..", "..", ledger.ControlDirName), link); err != nil {
		t.Fatalf("planting the within-root symlink: %v", err)
	}
	throughLink := "/artifacts/steps/unheld-handle/out/link/" +
		"source-88888888-8888-4888-8888-888888888888.json"
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete,
	} {
		if code := do(method, throughLink, []byte("garbage")); code < 400 || code > 499 {
			t.Errorf("%s of the hold record through a within-root symlink answered %d", method, code)
		}
	}
	// A record that does not exist yet, so a refusal cannot be "no such file".
	if code := do(http.MethodPut, "/artifacts/steps/unheld-handle/out/link/source-planted.json",
		[]byte("garbage")); code < 400 || code > 499 {
		t.Errorf("a NEW control record written through the symlink answered %d", code)
	}
	// And the link itself is not deletable through the API either: the key
	// names it, and the resolved path is the control directory.
	if code := do(http.MethodDelete, "/artifacts/steps/unheld-handle/out/link", nil); code < 400 || code > 499 {
		t.Errorf("deleting the planted link through the ordinary API answered %d", code)
	}

	// An ordered list rather than a map: a row that got through would destroy
	// what a later row names, and a random order would make the failure a
	// different one each run.
	for _, row := range []struct{ name, target string }{
		{"the record", "/artifacts/" + recordKey},
		{"a path under it", "/artifacts/" + ledger.ControlDirName + "/quarantine"},
		{"the directory", "/artifacts/" + ledger.ControlDirName},
		// Folded, because APFS and NTFS fold and an exact-string check has let
		// a structural name through here before (rejectStructuralName's own
		// comment).
		{"the directory, folded", "/artifacts/" + strings.ToUpper(ledger.ControlDirName)},
	} {
		for _, method := range []string{
			http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete,
		} {
			code := do(method, row.target, []byte("not a control record"))
			if code < 400 || code > 499 {
				t.Errorf("%s %s (%s) answered %d; the ordinary API does not serve the output "+
					"plane's ledger", method, row.target, row.name, code)
			}
		}
	}

	// The ledger is byte-for-byte what it was, and nothing new is beside it.
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("the hold record is gone: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the hold record was rewritten through the ordinary API: %q", after)
	}
	entries, err := os.ReadDir(filepath.Join(storage, ledger.ControlDirName))
	if err != nil {
		t.Fatalf("reading the control directory: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("the control directory holds %v; the ordinary API wrote into it", names)
	}

	// And the classifier still answers, from the same server: the held source
	// is refused and an unheld one is not.
	if code := do(http.MethodDelete, "/artifacts/steps/"+heldStepDir(), nil); code != http.StatusConflict {
		t.Errorf("after the control directory was refused, the held source answered %d", code)
	}
	stillThere(t, storage, heldIncarnation())
	if code := do(http.MethodDelete, "/artifacts/steps/unheld-handle", nil); code != http.StatusNoContent {
		t.Errorf("an unheld step directory answered %d; the guard became an outage", code)
	}
}

// The classification route the cleanup init container asks.
//
// It exists because the `rm -rf` init container is the one destructive path on
// a node that consulted nothing, and it cannot consult the way every other
// caller does: an init container holds no client certificate, so /artifacts/ is
// closed to it. So it asks this, and the script refuses to remove anything the
// answer does not call unmanaged.
//
// The pair is the whole test: a held handle answers `held` and an unheld one
// answers `unmanaged`. A route that said `held` about everything would be an
// outage dressed as a guard, and one that said `unmanaged` about everything
// would be the exposure it was written to close.
func TestTheCaptureClassRouteAnswersHeldAndUnheldAndDestroysNothing(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	ask := func(target string) (int, string) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

		return recorder.Code, recorder.Body.String()
	}

	code, body := ask("/capture-held/steps/" + heldStepDir())
	if code != http.StatusOK {
		t.Fatalf("asking about a held step answered %d: %s", code, body)
	}
	if !strings.Contains(body, `"class":"held"`) {
		t.Errorf("a capture-held step directory classified as %s", body)
	}

	code, body = ask("/capture-held/steps/unheld-handle")
	if code != http.StatusOK {
		t.Fatalf("asking about an unheld step answered %d: %s", code, body)
	}
	if !strings.Contains(body, `"class":"unmanaged"`) {
		t.Errorf("an unheld step directory classified as %s; the cleanup init would refuse to "+
			"clear a workspace nothing holds, which is an outage rather than a guard", body)
	}

	// It reads. It does not remove, and it does not reach outside steps/.
	stillThere(t, storage, heldIncarnation())
	if code, body := ask("/capture-held/steps/../" + ledger.ControlDirName); code == http.StatusOK &&
		!strings.Contains(body, `"class"`) {
		t.Errorf("a traversal out of steps/ was served: %d %s", code, body)
	}
	entries, err := os.ReadDir(filepath.Join(storage, ledger.ControlDirName))
	if err != nil || len(entries) != 1 {
		t.Errorf("the control directory holds %d entries after the classification route was "+
			"asked (err %v)", len(entries), err)
	}
}

// A READ-ONLY alias onto a held incarnation is admitted, and it is the only
// kind that is.
//
// Reviewer's F4, and the ruling: capture is ADDITIVE. A capture-selected task's
// Pod mounts the reserved incarnation as its declared output's volume, so
// `steps/<handle>/<output>` is a sibling directory nothing wrote into -- and
// with the write-capable alias refused (correctly) and no read-only one
// admitted, a downstream step consuming that output fetched an empty directory
// and nothing said so.
//
// The three arms are the whole rule. A write-capable alias onto the held source
// is still refused; a read-only one is created; and a read-only REMAP of a key
// that currently names the held source is still refused, because pointing that
// key elsewhere destroys the only way anything finds those bytes and no amount
// of read-only-ness makes that safe.
func TestAReadOnlyAliasOntoACaptureHeldSourceIsAdmittedAndAWriteCapableOneIsNot(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	register := func(key, localPath string, readOnly bool) int {
		body, err := json.Marshal(artifactwire.RegisterRequest{
			Key: key, LocalPath: localPath, ReadOnly: readOnly,
		})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	held := filepath.Join(storage, "steps", heldIncarnation())
	unheld := filepath.Join(storage, "steps", "unheld-handle", "out")

	// The control, and it is the arm that keeps this from weakening the guard:
	// a write-capable alias onto the same path is still a conflict.
	if code := register("a-write-capable-name", held, false); code != http.StatusConflict {
		t.Fatalf("a write-capable alias onto a capture-held source answered %d", code)
	}

	// The read-only one is created, and it resolves to the incarnation.
	if code := register("the-outputs-ordinary-key", held, true); code != http.StatusCreated {
		t.Fatalf("a read-only alias onto a capture-held source answered %d; a captured output "+
			"is still an ordinary output and a downstream step must be able to fetch it", code)
	}
	rel, found := server.registry.Lookup("the-outputs-ordinary-key")
	if !found || !strings.HasSuffix(string(rel), heldIncarnation()) {
		t.Errorf("the read-only alias resolves to %q and the incarnation is %q",
			rel, heldIncarnation())
	}

	// And a read-only REMAP off the held source is still refused. The mode says
	// what the NEW end may be; it says nothing about unmapping the old one.
	if code := register("the-outputs-ordinary-key", unheld, true); code != http.StatusConflict {
		t.Errorf("a read-only remap off a capture-held source answered %d", code)
	}
	if rel, found := server.registry.Lookup("the-outputs-ordinary-key"); !found ||
		!strings.HasSuffix(string(rel), heldIncarnation()) {
		t.Errorf("a refused read-only remap moved the alias to %q", rel)
	}
}
