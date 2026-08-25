package main_test

// Request-boundary containment (Track 5 of artifact_extraction_containment).
//
// Every track before this one guarded what an archive ENTRY may do. None
// guarded what the REQUEST may do, and the request key becomes a filesystem
// path before any archive is read. Go's ServeMux cleans the UNESCAPED path, so
// %2e%2e%2f survives routing and arrives in r.URL.Path as "../". A literal
// "../" does not — the mux 301s it — so the encoded form is the vector and is
// what these tests send.
//
// Each test asserts the state OUTSIDE the destination as well as the status.
// Asserting only the status would pass against an implementation that refused
// after it had already written or removed something.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// encodeTraversal percent-encodes "." and "/" so path cleaning at the mux
// cannot collapse the traversal before the handler sees it.
func encodeTraversal(rel string) string {
	var b strings.Builder
	for _, c := range rel {
		switch c {
		case '.':
			b.WriteString("%2e")
		case '/':
			b.WriteString("%2f")
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func tarWithOneFile(t *testing.T, name, content string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// ---------------------------------------------------------------------------
// AC1 — DELETE must not remove a tree outside the storage root.
// ---------------------------------------------------------------------------

func TestRequestContainment_DeleteArtifact_TraversalRefused(t *testing.T) {
	ts, storagePath := setupServer(t)

	outside := t.TempDir()
	victimDir := filepath.Join(outside, "important")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(victimDir, "data.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	rel, err := filepath.Rel(storagePath, victimDir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/"+encodeTraversal(rel), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a traversing key, got %d", resp.StatusCode)
	}
	if _, err := os.Stat(victim); os.IsNotExist(err) {
		t.Errorf("ESCAPE: tree outside the storage root was removed (status %d)", resp.StatusCode)
	}
}

// Zero-case: a real key still deletes. Without this the validator could pass
// by refusing everything.
func TestRequestContainment_DeleteArtifact_RealKeyStillWorks(t *testing.T) {
	ts, storagePath := setupServer(t)

	dir := filepath.Join(storagePath, "steps", "build-1", "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/steps/build-1/out", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for a legitimate three-segment key, got %d", resp.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("legitimate delete did not remove %s", dir)
	}
}

// ---------------------------------------------------------------------------
// AC2 — stream-in must not write or delete outside steps/.
// ---------------------------------------------------------------------------

func TestRequestContainment_StreamIn_TraversalRefused(t *testing.T) {
	ts, storagePath := setupServer(t)

	victimDir := filepath.Join(storagePath, "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(victimDir, "precious.txt")
	if err := os.WriteFile(precious, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := tarWithOneFile(t, "planted.txt", "PWNED")
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/stream-in/"+encodeTraversal("../victim"), body)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a traversing key, got %d", resp.StatusCode)
	}
	if got, err := os.ReadFile(precious); err != nil || string(got) != "original" {
		t.Errorf("ESCAPE: pre-existing file outside steps/ was disturbed (got %q, err %v, status %d)",
			got, err, resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(victimDir, "planted.txt")); err == nil {
		t.Errorf("ESCAPE: payload planted outside steps/ (status %d)", resp.StatusCode)
	}
}

// AC7 — a refused stream-in must leave an EXISTING artifact byte-identical.
// Validation must happen before os.RemoveAll(dest), not after.
func TestRequestContainment_StreamIn_RefusalLeavesExistingArtifactIntact(t *testing.T) {
	ts, storagePath := setupServer(t)

	existing := filepath.Join(storagePath, "steps", "build-x", "out")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "keep.txt")
	if err := os.WriteFile(marker, []byte("KEEP-ME"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A traversing key that, unguarded, would RemoveAll before failing.
	body := tarWithOneFile(t, "x.txt", "x")
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/stream-in/"+encodeTraversal("../steps/build-x/out"), body)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()

	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "KEEP-ME" {
		t.Errorf("refused stream-in disturbed an existing artifact (got %q, err %v, status %d)",
			got, err, resp.StatusCode)
	}
}

// Zero-case for AC7: a PERMITTED overwrite must still replace, so the guard
// cannot pass by refusing every overwrite.
func TestRequestContainment_StreamIn_PermittedOverwriteStillReplaces(t *testing.T) {
	ts, storagePath := setupServer(t)

	existing := filepath.Join(storagePath, "steps", "build-y", "out")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "old.txt"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := tarWithOneFile(t, "new.txt", "NEW")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/build-y/out", body)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for a legitimate overwrite, got %d", resp.StatusCode)
	}
	if got, err := os.ReadFile(filepath.Join(existing, "new.txt")); err != nil || string(got) != "NEW" {
		t.Errorf("legitimate overwrite did not land: got %q err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(existing, "old.txt")); err == nil {
		t.Errorf("legitimate overwrite left the previous tree in place")
	}
}

// ---------------------------------------------------------------------------
// AC3 / AC4 — /register must not alias a path outside the storage root, and a
// refusal must not persist to aliases.json.
// ---------------------------------------------------------------------------

func TestRequestContainment_Register_LocalPathOutsideRootRefused(t *testing.T) {
	ts, storagePath := setupServer(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"key": "pwn", "local_path": secret})
	resp, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a local_path outside the storage root, got %d", resp.StatusCode)
	}

	r2, err := http.Get(ts.URL + "/artifacts/pwn")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer r2.Body.Close()
	got, _ := io.ReadAll(r2.Body)
	if bytes.Contains(got, []byte("TOP-SECRET")) {
		t.Errorf("ARBITRARY READ: file outside the storage root served to the caller")
	}

	// AC4: the refusal must not have been persisted. Asserted against the file
	// on disk, not the in-memory registry — the defect is that it survives a
	// restart.
	if data, err := os.ReadFile(filepath.Join(storagePath, "aliases.json")); err == nil {
		if bytes.Contains(data, []byte("pwn")) {
			t.Errorf("refused register was persisted to aliases.json: %s", data)
		}
	}
}

func TestRequestContainment_Register_LocalPathInsideRootAccepted(t *testing.T) {
	ts, storagePath := setupServer(t)

	inside := filepath.Join(storagePath, "steps", "build-ok", "out")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "f.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"key": "good", "local_path": inside})
	resp, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 for a contained local_path, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// AC5 — /resolve must not write or delete outside the storage root. This is
// the mTLS-EXEMPT endpoint, so it is reachable without a client certificate.
// ---------------------------------------------------------------------------

func TestRequestContainment_Resolve_DestOutsideRootRefused(t *testing.T) {
	ts, storagePath := setupServer(t)

	src := filepath.Join(storagePath, "steps", "srcbuild", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "payload.txt"), []byte("PAYLOAD"), 0o644); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	guard := filepath.Join(outside, "do-not-touch.txt")
	if err := os.WriteFile(guard, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(outside, "attacker-chosen")

	body, _ := json.Marshal(map[string]string{"key": "srcbuild/out", "dest": dest})
	resp, err := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a dest outside the storage root, got %d", resp.StatusCode)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Errorf("ESCAPE: dest created outside the storage root (status %d)", resp.StatusCode)
	}
	if got, err := os.ReadFile(guard); err != nil || string(got) != "original" {
		t.Errorf("ESCAPE: neighbouring file disturbed (got %q err %v)", got, err)
	}
}

func TestRequestContainment_Resolve_DestInsideRootAccepted(t *testing.T) {
	ts, storagePath := setupServer(t)

	src := filepath.Join(storagePath, "steps", "srcbuild2", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "payload.txt"), []byte("PAYLOAD"), 0o644); err != nil {
		t.Fatal(err)
	}
	// copyArtifact creates its temp dir as a SIBLING of dest, so dest's parent
	// must exist. The pre-existing specs get this free from t.TempDir(); a
	// migrated dest under the storage root has to create it.
	if err := os.MkdirAll(filepath.Join(storagePath, "resolved"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(storagePath, "resolved", "input")

	body, _ := json.Marshal(map[string]string{"key": "srcbuild2/out", "dest": dest})
	resp, err := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for a contained dest, got %d", resp.StatusCode)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "payload.txt")); err != nil || string(got) != "PAYLOAD" {
		t.Errorf("contained resolve did not deliver: got %q err %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// AC6 — /mirror must not tar a tree outside the storage root and ship it.
// ---------------------------------------------------------------------------

func TestRequestContainment_MirrorTrigger_TraversalRefused(t *testing.T) {
	ts, _ := setupServer(t)

	body, _ := json.Marshal(map[string]string{"key": "../../etc"})
	resp, err := http.Post(ts.URL+"/mirror", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mirror: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a traversing mirror key, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// AC8 — validation reads the DECODED path, and a benign percent-encoding must
// still resolve. Without this the validator could pass by rejecting all
// encoded input.
// ---------------------------------------------------------------------------

func TestRequestContainment_BenignPercentEncodingStillResolves(t *testing.T) {
	ts, storagePath := setupServer(t)

	// %2D decodes to '-', which is ordinary in these keys.
	dir := filepath.Join(storagePath, "steps", "build-42", "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/artifacts/steps/build%2D42/out")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		t.Errorf("benign percent-encoding was refused: status %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// AC10 — the three-segment key shape must survive. This is what
// durable.ValidateKey would reject, and it is ordinary production traffic.
// ---------------------------------------------------------------------------

func TestRequestContainment_ThreeSegmentKeysAccepted(t *testing.T) {
	ts, storagePath := setupServer(t)

	for _, key := range []string{
		"build-42-output.tar",
		"steps/build-99",
		"steps/build-42/result",
		"caches/job-42/build-abc.tar",
	} {
		full := filepath.Join(storagePath, key)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		resp, err := http.Head(ts.URL + "/artifacts/" + key)
		if err != nil {
			t.Fatalf("HEAD %s: %v", key, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Errorf("legitimate key %q refused: status %d", key, resp.StatusCode)
		}
	}
}

// /resolve-batch is the least authenticated way into resolveOne — it is
// mTLS-exempt and takes a key and dest per item. It was unvalidated in the
// first cut of this track; the AC11 re-derivation caught it, not a review.
func TestRequestContainment_ResolveBatch_DestOutsideRootRefused(t *testing.T) {
	ts, storagePath := setupServer(t)

	src := filepath.Join(storagePath, "steps", "batchsrc", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	bad := filepath.Join(outside, "escaped")

	body, _ := json.Marshal(map[string]any{
		"items": []map[string]string{{"key": "batchsrc/out", "dest": bad}},
	})
	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for a batch dest outside the storage root, got %d", resp.StatusCode)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Errorf("ESCAPE: batch wrote outside the storage root (status %d)", resp.StatusCode)
	}
}

// A refused item must not let EARLIER items run — refusal precedes side effects.
func TestRequestContainment_ResolveBatch_RefusesWholeBatchBeforeAnyCopy(t *testing.T) {
	ts, storagePath := setupServer(t)

	src := filepath.Join(storagePath, "steps", "batchsrc2", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storagePath, "resolved"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(storagePath, "resolved", "good")
	bad := filepath.Join(t.TempDir(), "escaped")

	body, _ := json.Marshal(map[string]any{
		"items": []map[string]string{
			{"key": "batchsrc2/out", "dest": good},
			{"key": "batchsrc2/out", "dest": bad},
		},
	})
	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	if _, err := os.Stat(good); err == nil {
		t.Errorf("first item was copied despite a later item being refused (status %d)", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Bypass regressions. Every one of these PASSED against the first cut of this
// track and was found by an adversarial review, not by the author. They stay
// as the record of what the rule missed.
// ---------------------------------------------------------------------------

// B2: does validateRequestKey accept "."?
func TestBypass_DotKeyDestroysWholeStore(t *testing.T) {
	ts, storagePath := setupServer(t)
	keep := filepath.Join(storagePath, "steps", "keepme", "out")
	os.MkdirAll(keep, 0o755)
	os.WriteFile(filepath.Join(keep, "f.txt"), []byte("KEEP"), 0o644)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/%2e", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	t.Logf("DELETE /artifacts/%%2e -> %d", resp.StatusCode)
	if _, err := os.Stat(keep); os.IsNotExist(err) {
		t.Errorf("B2 CONFIRMED: whole storage root destroyed by a single request")
	}
}

// B1: does validateContainedPath accept dest == root?
func TestBypass_DestEqualsRoot(t *testing.T) {
	ts, storagePath := setupServer(t)
	src := filepath.Join(storagePath, "steps", "s", "out")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644)
	keep := filepath.Join(storagePath, "steps", "keep2")
	os.MkdirAll(keep, 0o755)

	b, _ := json.Marshal(map[string]string{"key": "s/out", "dest": storagePath})
	resp, _ := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(b))
	resp.Body.Close()
	t.Logf("POST /resolve dest=<root> -> %d", resp.StatusCode)
	if _, err := os.Stat(keep); os.IsNotExist(err) {
		t.Errorf("B1 CONFIRMED: dest==root removed the whole root")
	}
}

// B3: plant an absolute symlink via the (still-uncontained) stream-in extractor,
// then try to read through it.
func TestBypass_ReadThroughPlantedSymlink(t *testing.T) {
	ts, _ := setupServer(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("TOP-SECRET-NODE-FILE"), 0o600)

	var buf bytes.Buffer
	tw := newTar(&buf)
	tw.symlink("pwn", secret)
	tw.close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/evil", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	r1, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	t.Logf("stream-in -> %d", r1.StatusCode)

	r2, err := http.Get(ts.URL + "/artifacts/steps/evil/pwn")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	// io.ReadAll, not a single Read: a short read of 1 byte would make this
	// pass regardless of what the server returned.
	body, err := io.ReadAll(r2.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("GET -> %d body=%q", r2.StatusCode, string(body))
	if bytes.Contains(body, []byte("TOP-SECRET")) {
		t.Errorf("B3 CONFIRMED: arbitrary read through a planted symlink")
	}
}

type tarHelper struct{ w *tar.Writer }

func newTar(b *bytes.Buffer) *tarHelper { return &tarHelper{tar.NewWriter(b)} }
func (h *tarHelper) symlink(name, target string) {
	h.w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777})
}
func (h *tarHelper) close() { h.w.Close() }

// A stream-in must not escape steps/ into the store root, even though the store
// root is still "inside the daemon's storage".
//
// artifactLocation first validated against s.storagePath rather than the root
// its caller passed, which made containment vacuous for any caller with a
// narrower boundary. stream-in passes storagePath/steps, so a symlink planted
// under steps/ pointing at the store root let
// PUT /stream-in/x/link/aliases.json destroy the alias file (201).
func TestRequestContainment_StreamInCannotEscapeStepsIntoStoreRoot(t *testing.T) {
	ts, storagePath := setupServer(t)

	// A file at the store root that must not be writable by a stream-in.
	sentinel := filepath.Join(storagePath, "aliases.json")
	if err := os.WriteFile(sentinel, []byte(`{"legit":"data"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. plant steps/x/link -> <storagePath>
	var b1 bytes.Buffer
	tw := tar.NewWriter(&b1)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: storagePath, Mode: 0o777})
	tw.Close()
	r1, err := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/x", &b1)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := http.DefaultClient.Do(r1)
	if err != nil {
		t.Fatal(err)
	}
	p1.Body.Close()
	t.Logf("plant steps/x/link -> storagePath : %d", p1.StatusCode)

	// 2. stream in through it, targeting the store root's aliases.json
	var b2 bytes.Buffer
	tw2 := tar.NewWriter(&b2)
	body := []byte("POISONED")
	tw2.WriteHeader(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644})
	tw2.Write(body)
	tw2.Close()
	r2, err := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/x/link/aliases.json", &b2)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	p2.Body.Close()
	t.Logf("stream-in through link to store root : %d", p2.StatusCode)

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Errorf("PROBE HIT: aliases.json at the store root was destroyed (%v)", err)
		return
	}
	if !bytes.Contains(got, []byte("legit")) {
		t.Errorf("PROBE HIT: aliases.json overwritten, now %q", got)
	}
}

// ---------------------------------------------------------------------------
// Round-5 regressions. All five passed against the previous commit. Four of the
// five were the SAME defect as an earlier round, at a site the earlier
// reproduction had not used.
// ---------------------------------------------------------------------------

// R5-1: /resolve step 1 copies from a registry path without validating it, then
// serves the result. mTLS-exempt.
func TestRequestContainment_ResolveRegistryBranchIsValidated(t *testing.T) {
	ts, storagePath := setupServer(t)

	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "loot.txt"), []byte("HOST-SECRET"), 0o600)

	os.MkdirAll(filepath.Join(storagePath, "steps", "a1"), 0o755)
	real := filepath.Join(storagePath, "steps", "a1", "d")
	os.MkdirAll(real, 0o755)
	body, _ := json.Marshal(map[string]string{"key": "alias1", "local_path": real})
	rr, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()

	// Swap the registered target for a link pointing out.
	os.RemoveAll(real)
	if err := os.Symlink(outside, real); err != nil {
		t.Fatal(err)
	}

	os.MkdirAll(filepath.Join(storagePath, "resolved"), 0o755)
	dest := filepath.Join(storagePath, "resolved", "d1")
	rb, _ := json.Marshal(map[string]string{"key": "alias1", "dest": dest})
	resp, err := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(rb))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got, err := os.ReadFile(filepath.Join(dest, "loot.txt")); err == nil {
		t.Errorf("R5-1: /resolve copied from outside the root into the store: %q", got)
	}
}

// R5-2 / R5-3: the resource-cache routes read the registry, and a poisoned
// alias must not be served there either.
func TestRequestContainment_ResourceCacheRoutesValidateRegistry(t *testing.T) {
	ts, storagePath := setupServer(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "rc.txt")
	os.WriteFile(secret, []byte("RC-HOST-SECRET"), 0o600)

	os.MkdirAll(filepath.Join(storagePath, "steps", "rc"), 0o755)
	real := filepath.Join(storagePath, "steps", "rc", "f")
	os.WriteFile(real, []byte("ok"), 0o644)
	body, _ := json.Marshal(map[string]string{"key": "rc-9", "local_path": real})
	rr, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()

	os.Remove(real)
	if err := os.Symlink(secret, real); err != nil {
		t.Fatal(err)
	}

	g, err := http.Get(ts.URL + "/resource-caches/rc-9")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Body.Close()
	got, _ := io.ReadAll(g.Body)
	if bytes.Contains(got, []byte("RC-HOST-SECRET")) {
		t.Errorf("R5-2: GET /resource-caches/ served a path outside the root")
	}

	h, err := http.Head(ts.URL + "/resource-caches/rc-9")
	if err != nil {
		t.Fatal(err)
	}
	h.Body.Close()
	if h.StatusCode == http.StatusOK {
		t.Errorf("R5-3: HEAD /resource-caches/ advertised an out-of-root path as node-local")
	}
}

// R5-4: aliases.json is a structural file at the store root, not an artifact.
func TestRequestContainment_AliasStoreIsNotAddressable(t *testing.T) {
	ts, storagePath := setupServer(t)
	sentinel := filepath.Join(storagePath, "aliases.json")
	os.WriteFile(sentinel, []byte(`{"legit":"data"}`), 0o644)

	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req, err := http.NewRequest(m, ts.URL+"/artifacts/aliases.json", bytes.NewReader([]byte("x")))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("R5-4: %s /artifacts/aliases.json -> %d, want 4xx", m, resp.StatusCode)
		}
	}
	if got, err := os.ReadFile(sentinel); err != nil || !bytes.Contains(got, []byte("legit")) {
		t.Errorf("R5-4: the alias store was modified or destroyed (got %q, err %v)", got, err)
	}
}

// R5-4b: structural names are compared case-insensitively, because APFS and
// NTFS fold case and an exact-string map let DELETE /artifacts/STEPS through.
func TestRequestContainment_StructuralNamesAreCaseFolded(t *testing.T) {
	ts, storagePath := setupServer(t)
	keep := filepath.Join(storagePath, "steps", "b", "out")
	os.MkdirAll(keep, 0o755)

	for _, variant := range []string{"STEPS", "Steps", "sTePs", "ALIASES.JSON"} {
		req, err := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/"+variant, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("DELETE /artifacts/%s -> %d, want 4xx", variant, resp.StatusCode)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a case variant of a structural name destroyed the store")
	}
}

// AC1b — "." must be refused on EVERY per-artifact verb, and on /mirror.
//
// os.Root refuses "." only for Remove/RemoveAll. Root.Stat(".") and
// Root.Open(".") succeed and enumerate the whole store. An earlier draft of
// this track's spec deleted validateRequestKey's "." check on the stated
// grounds that os.Root refused it — which was false and unverified. The
// existing bypass regression covers only DELETE, the one verb that happens to
// fail safe, so it would have shipped green.
func TestRequestContainment_DotRefusedOnEveryVerb(t *testing.T) {
	ts, storagePath := setupServer(t)

	keep := filepath.Join(storagePath, "steps", "keep", "out")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "f.txt"), []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "aliases.json"), []byte(`{"a":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(m, ts.URL+"/artifacts/%2e", bytes.NewReader([]byte("x")))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf(`%s /artifacts/%%2e -> %d, want 4xx`, m, resp.StatusCode)
		}
		if bytes.Contains(body, []byte("aliases.json")) {
			t.Errorf("%s /artifacts/%%2e leaked the store listing", m)
		}
	}

	mb, _ := json.Marshal(map[string]string{"key": "."})
	resp, err := http.Post(ts.URL+"/mirror", "application/json", bytes.NewReader(mb))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf(`POST /mirror {"key":"."} -> %d, want 4xx`, resp.StatusCode)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the store was damaged by a '.' request: %v", err)
	}

	// Zero-case: an ordinary key still works on each verb.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(m, ts.URL+"/artifacts/steps/keep/out/f.txt", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s of an ordinary key -> %d, want 200", m, resp.StatusCode)
		}
	}
}
