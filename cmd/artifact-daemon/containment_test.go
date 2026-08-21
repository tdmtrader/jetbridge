package main_test

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

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
	peerServer := daemon.NewServer(peerLogger, peerStorage, "peer-node")
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
