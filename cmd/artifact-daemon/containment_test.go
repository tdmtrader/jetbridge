package main_test

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// Containment tests for tar extraction. An archive arriving from a peer or the
// shared bucket is untrusted input: it may name any path and point a symlink
// anywhere. None of that may cause a write outside the destination, and no
// symlink leaving the destination may survive on disk for a later consumer to
// follow.
//
// These exercise the real PeerResolver.Fetch against a real httptest server,
// following TestPeerFetch_DownloadsAndExtractsTar (peers_test.go:24).

// tarEntry is one header plus optional body.
type tarEntry struct {
	hdr  *tar.Header
	body string
}

// serveTar stands up a peer that returns the given entries as a tar stream.
func serveTar(t *testing.T, entries ...tarEntry) (host string, port int) {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if e.hdr.Typeflag == tar.TypeReg {
			e.hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(e.hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", e.hdr.Name, err)
		}
		if e.body != "" {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatalf("write body for %q: %v", e.hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(buf.Bytes())
	}))
	t.Cleanup(ts.Close)

	return splitHostPort(t, ts.Listener.Addr().String())
}

// fetchInto runs a real Fetch of the served archive into a fresh directory.
func fetchInto(t *testing.T, host string, port int) (destDir string, err error) {
	t.Helper()
	logger := lagertest.NewTestLogger("containment")
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)
	destDir = filepath.Join(t.TempDir(), "extract")
	return destDir, resolver.Fetch(t.Context(), host, "containment-key", destDir)
}

// guardedFile writes a file outside any destination and returns its path plus
// a checker asserting it was not modified.
func guardedFile(t *testing.T) (path string, assertUntouched func()) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	return path, func() {
		t.Helper()
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("victim unreadable: %v", readErr)
		}
		if string(got) != "original" {
			t.Errorf("ESCAPE: file outside destination was modified; got %q, want %q", string(got), "original")
		}
	}
}

// symlinksUnder returns every symlink found beneath root.
func symlinksUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// AC 1 / AC 6 — the reproduced escape: a symlink out, then a write through it.
func TestPeerFetch_SymlinkEscape_Refused(t *testing.T) {
	victim, assertUntouched := guardedFile(t)
	outside := filepath.Dir(victim)

	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0777}},
		tarEntry{hdr: &tar.Header{Name: "hatch/victim.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "PWNED"},
	)

	_, err := fetchInto(t, host, port)

	assertUntouched()
	if err == nil {
		t.Error("Fetch returned nil for an archive that escapes its destination")
	}
}

// AC 2 — a name that walks upward out of the destination.
func TestPeerFetch_NameTraversal_Refused(t *testing.T) {
	victim, assertUntouched := guardedFile(t)

	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "../../../../../../.." + victim, Typeflag: tar.TypeReg, Mode: 0644}, body: "PWNED"},
	)

	_, err := fetchInto(t, host, port)

	assertUntouched()
	if err == nil {
		t.Error("Fetch returned nil for an archive whose entry name traverses out of the destination")
	}
}

// AC 4 — an absolute symlink target is refused, and no symlink is left behind.
func TestPeerFetch_AbsoluteSymlink_Refused(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0777}},
	)

	destDir, err := fetchInto(t, host, port)

	if err == nil {
		t.Error("Fetch returned nil for an archive containing an absolute symlink target")
	}
	if links := symlinksUnder(t, destDir); len(links) != 0 {
		t.Errorf("LANDMINE: symlink(s) left on disk after a refused extraction: %v", links)
	}
}

// AC 5 — a chain whose composition escapes, even though each hop looks local.
func TestPeerFetch_SymlinkChain_Refused(t *testing.T) {
	victim, assertUntouched := guardedFile(t)
	outside := filepath.Dir(victim)

	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "b", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0777}},
		tarEntry{hdr: &tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "b", Mode: 0777}},
		tarEntry{hdr: &tar.Header{Name: "a/victim.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "PWNED"},
	)

	destDir, err := fetchInto(t, host, port)

	assertUntouched()
	if err == nil {
		t.Error("Fetch returned nil for an archive whose symlink chain escapes")
	}
	for _, l := range symlinksUnder(t, destDir) {
		target, _ := os.Readlink(l)
		if filepath.IsAbs(target) {
			t.Errorf("LANDMINE: absolute symlink left on disk: %s -> %s", l, target)
		}
	}
}

// AC 3 — a relative symlink that stays inside must survive, produced by the
// daemon's OWN tar producer so this is a genuine round trip.
func TestPeerFetch_InternalSymlinkPreserved(t *testing.T) {
	peerStorage := t.TempDir()
	stepDir := filepath.Join(peerStorage, "steps", "handle-x", "result")
	shared := filepath.Join(stepDir, "shared")
	if err := os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "dep.txt"), []byte("shared dep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stepDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	// The case this whole rule exists to permit: two trees in one artifact
	// sharing a dependency directory by relative link.
	if err := os.Symlink("../shared", filepath.Join(stepDir, "app", "node_modules")); err != nil {
		t.Fatal(err)
	}

	peerLogger := lagertest.NewTestLogger("peer")
	peerServer := newDaemonServer(t, peerLogger, peerStorage, "peer-node")
	peerTS := httptest.NewServer(peerServer.Handler())
	defer peerTS.Close()

	host, port := splitHostPort(t, peerTS.Listener.Addr().String())
	logger := lagertest.NewTestLogger("containment")
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "fetched")
	if err := resolver.Fetch(t.Context(), host, "handle-x/result", destDir); err != nil {
		t.Fatalf("Fetch of an artifact with a legitimate internal symlink failed: %v", err)
	}

	link := filepath.Join(destDir, "app", "node_modules")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("internal symlink not extracted: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink after extraction (mode %v)", link, info.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != "../shared" {
		t.Errorf("symlink target rewritten: got %q, want %q", target, "../shared")
	}
}

// --- Regressions from the adversarial review of ab1c66c2c5 ---

// F1 — a hard link is a real entry, not something to drop on the floor. The
// switch previously handled Dir/Reg/Symlink only, so a TypeLink entry vanished
// and the extraction still reported success.
func TestPeerFetch_HardLinkIsMaterialized(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "real.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "data"},
		tarEntry{hdr: &tar.Header{Name: "hard.txt", Typeflag: tar.TypeLink, Linkname: "real.txt", Mode: 0644}},
	)

	destDir, err := fetchInto(t, host, port)
	if err != nil {
		t.Fatalf("Fetch of an archive with a legitimate hard link failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hard.txt"))
	if err != nil {
		t.Fatalf("SILENT DATA LOSS: hard link entry absent after a successful extraction: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("hard link content = %q, want %q", got, "data")
	}
}

// F1 — a hard link whose target escapes takes the same rule as a symlink.
func TestPeerFetch_HardLinkEscape_Refused(t *testing.T) {
	victim, assertUntouched := guardedFile(t)

	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "hard.txt", Typeflag: tar.TypeLink, Linkname: victim, Mode: 0644}},
	)

	destDir, err := fetchInto(t, host, port)

	assertUntouched()
	if err == nil {
		t.Error("Fetch returned nil for a hard link targeting a file outside the destination")
	}
	if _, serr := os.Stat(filepath.Join(destDir, "hard.txt")); serr == nil {
		t.Error("ESCAPE: hard link to a file outside the destination was created")
	}
}

// F1 — an entry type the daemon cannot materialize fails loudly rather than
// being skipped into a tree the caller thinks is complete.
func TestPeerFetch_UnsupportedEntryType_Refused(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "dev", Typeflag: tar.TypeChar, Mode: 0666, Devmajor: 1, Devminor: 3}},
	)

	_, err := fetchInto(t, host, port)
	if err == nil {
		t.Error("Fetch returned nil for an archive containing a character-device entry")
	}
}

// F2 — a refused extraction leaves nothing at the destination, not even the
// entries that preceded the refusal.
func TestPeerFetch_RefusalLeavesNoResidue(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "good.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "legit"},
		tarEntry{hdr: &tar.Header{Name: "sub/also-good.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "legit"},
		tarEntry{hdr: &tar.Header{Name: "bad", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0777}},
	)

	destDir, err := fetchInto(t, host, port)
	if err == nil {
		t.Fatal("expected the archive to be refused")
	}

	if _, serr := os.Stat(destDir); !os.IsNotExist(serr) {
		var left []string
		filepath.Walk(destDir, func(p string, info os.FileInfo, e error) error {
			if e == nil && !info.IsDir() {
				rel, _ := filepath.Rel(destDir, p)
				left = append(left, rel)
			}
			return nil
		})
		t.Errorf("PARTIAL RESIDUE: a refused extraction left %v at the destination", left)
	}

	// The temp directory must be cleaned too, not merely unpromoted.
	parent := filepath.Dir(destDir)
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Errorf("refused extraction left temp residue: %s", e.Name())
		}
	}
}

// F3 — the error the operator finally sees must name the real cause, not a
// spurious "file exists" produced by a retry re-running over its own residue.
func TestPeerFetch_RetryReportsTheRealCause(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "first.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "x"},
		tarEntry{hdr: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "first.txt", Mode: 0777}},
		tarEntry{hdr: &tar.Header{Name: "bad", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0777}},
	)

	_, err := fetchInto(t, host, port)
	if err == nil {
		t.Fatal("expected the archive to be refused")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("MASKED CAUSE: error does not name the real reason; got: %v", err)
	}
	if strings.Contains(err.Error(), "file exists") {
		t.Errorf("MASKED CAUSE: error is a retry artifact rather than the real reason; got: %v", err)
	}
}

// Round-two regression: a Fetch into a destination that already holds unrelated
// content must deliver the fetched artifact, not report success and leave the
// stale bytes in place. An earlier revision treated a rename onto an existing
// directory as success — a policy borrowed from DurableTier.Restore, whose
// destination corresponds to a bucket key. This destination is chosen by the
// caller, so "something is already here" does not mean "this artifact is
// already here".
func TestPeerFetch_ReplacesUnrelatedExistingDestination(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "fetched.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "NEW"},
	)

	destDir := filepath.Join(t.TempDir(), "extract")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "stale.txt"), []byte("OLD"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := lagertest.NewTestLogger("containment")
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)
	if err := resolver.Fetch(t.Context(), host, "containment-key", destDir); err != nil {
		t.Fatalf("Fetch into an existing destination failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "fetched.txt"))
	if err != nil {
		t.Fatalf("SILENT NO-OP: Fetch reported success but did not deliver the artifact: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("fetched.txt = %q, want %q", got, "NEW")
	}
	if _, err := os.Stat(filepath.Join(destDir, "stale.txt")); err == nil {
		t.Error("unrelated content from the previous occupant survived the fetch")
	}
}

// Round-three regression: concurrent resolves of the same key into the same
// destination must all succeed and leave a complete artifact.
//
// The guarantee lives in the DAEMON, not in Fetch: resolveOne serialises
// resolves by destination, so concurrent peer fetches of one dest run one at a
// time and each promotion's clear-then-rename is private. An earlier revision
// put the guarantee inside Fetch instead, tolerating ErrExist/ENOTEMPTY on the
// rename as "a concurrent fetch of this same key won" — but Fetch cannot know
// that: dest is caller-supplied, distinct keys legitimately share one
// (TestResolveSem_BoundsBothResolveRoutes fires several), and a failed CLEAR
// produced the same ENOTEMPTY — so the tolerance also reported success while
// delivering whatever stale content it could not remove. This test therefore
// drives the real route; direct concurrent Fetch calls to one dest are no
// longer a supported contract.
func TestPeerFetch_ConcurrentFetchesIntoOneDestination(t *testing.T) {
	host, port := serveTar(t,
		tarEntry{hdr: &tar.Header{Name: "payload.txt", Typeflag: tar.TypeReg, Mode: 0644}, body: "content"},
	)

	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("containment")
	s := newDaemonServer(t, logger, storagePath, "local-node")

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{host}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
	})
	s.SetPeerResolver(daemon.NewPeerResolver(logger, clientset, "concourse", "artifact-daemon", port, "10.0.0.99", nil))

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	destDir := destUnder(t, storagePath, "shared-dest")
	body := `{"key":"containment-key","dest":"` + destDir + `"}`

	const concurrent = 8
	var wg sync.WaitGroup
	statuses := make([]int, concurrent)
	bodies := make([]string, concurrent)
	for i := range concurrent {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
			if err != nil {
				t.Errorf("concurrent resolve %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			statuses[i] = resp.StatusCode
			bodies[i] = string(b)
		}(i)
	}
	wg.Wait()

	for i := range concurrent {
		if statuses[i] != http.StatusOK {
			t.Errorf("concurrent resolve %d failed: %d %s", i, statuses[i], bodies[i])
		}
	}

	got, err := os.ReadFile(filepath.Join(destDir, "payload.txt"))
	if err != nil {
		t.Fatalf("destination incomplete after concurrent resolves: %v", err)
	}
	if string(got) != "content" {
		t.Errorf("payload = %q, want %q", got, "content")
	}
}
