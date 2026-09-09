package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
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
	sweeper.SetCaptureLedger(server.captureLedger)
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

	resolve := func(dest string) resolveResponse {
		body, err := json.Marshal(resolveRequest{Key: "some-artifact", Dest: dest})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		var answer resolveResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			// A structural 400 is plain text. Carry it through as the error so
			// the assertion below reads the same either way.
			return resolveResponse{Status: "error", Error: recorder.Body.String()}
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
		body, err := json.Marshal(registerRequest{Key: key, LocalPath: localPath})
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

	// Remap. The alias is placed while nothing holds the source is not
	// available here -- the hold predates the server -- so the mapping is
	// installed directly, which is what a remap starts from.
	if _, err := server.registry.RegisterAlias("the-captures-name", held); err != nil {
		t.Fatalf("seeding the mapping: %v", err)
	}
	if code := register("the-captures-name", unheld); code != http.StatusConflict {
		t.Errorf("remapping a capture-held source's alias elsewhere answered %d", code)
	}
	if rel, ok := server.registry.Lookup("the-captures-name"); !ok ||
		!strings.HasSuffix(string(rel), heldIncarnation()) {
		t.Errorf("the capture's alias now points at %q", rel)
	}
}
