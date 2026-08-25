package main_test

import (
	"archive/tar"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, url string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func tarOf(t *testing.T, hdrs []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for i, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) && bodies[i] != "" {
			tw.Write([]byte(bodies[i]))
		}
	}
	tw.Close()
	return b.Bytes()
}

func TestPhase1_Behaviours(t *testing.T) {
	// malformed tar must stay 400 (it is 400 today; the first draft of the
	// classification would have made it 500)
	t.Run("malformed tar is 400", func(t *testing.T) {
		ts, _ := setupServer(t)
		if got := put(t, ts.URL+"/stream-in/m/o", []byte("not a tar at all")); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// truncated archive: the likeliest corrupt input, surfaces from io.Copy
	t.Run("truncated archive is 400", func(t *testing.T) {
		ts, _ := setupServer(t)
		full := tarOf(t, []*tar.Header{
			{Name: "f", Typeflag: tar.TypeReg, Size: 4096, Mode: 0o644},
		}, []string{string(bytes.Repeat([]byte("x"), 4096))})
		if got := put(t, ts.URL+"/stream-in/t/o", full[:1500]); got != 400 {
			t.Errorf("got %d, want 400 (truncated is archive-attributable)", got)
		}
	})

	// R10 exception 1: absolute symlink now refused (was 201)
	t.Run("absolute symlink now refused", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/a/o", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// R10 exception 2: hard link now materialised (was silently dropped)
	t.Run("hard link now materialised", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "target.txt", Typeflag: tar.TypeReg, Size: 7, Mode: 0o644},
			{Name: "a/b/link", Typeflag: tar.TypeLink, Linkname: "target.txt", Mode: 0o644},
		}, []string{"payload"})
		if got := put(t, ts.URL+"/stream-in/h/o", b); got != 201 {
			t.Fatalf("got %d, want 201", got)
		}
		if _, err := os.Lstat(filepath.Join(storagePath, "steps", "h", "o", "a", "b", "link")); err != nil {
			t.Errorf("root-relative hard link not materialised: %v", err)
		}
	})

	// R10 exception 3: traversing entry now fails (was continue)
	t.Run("traversing entry now fails", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "../escape.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
		}, []string{"xx"})
		if got := put(t, ts.URL+"/stream-in/e/o", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// unchanged: an internal relative symlink still round-trips
	t.Run("internal symlink still round-trips", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "shared/pkg.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
			{Name: "app/node_modules", Typeflag: tar.TypeSymlink, Linkname: "../shared", Mode: 0o777},
		}, []string{"ok"})
		if got := put(t, ts.URL+"/stream-in/s/o", b); got != 201 {
			t.Fatalf("got %d, want 201 — containment became prohibition", got)
		}
		fi, err := os.Lstat(filepath.Join(storagePath, "steps", "s", "o", "app", "node_modules"))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("internal symlink lost: err=%v", err)
		}
	})
}

// AC6 / R2 — the steps/ boundary is a nested HANDLE, and containment must not
// have become prohibition.
func TestPhase3_NestedStepsBoundary(t *testing.T) {
	// An artifact's own internal symlinks are part of the payload format —
	// two worktrees sharing a dependency tree is the real case. A root handle
	// refuses traversal OUT; it must not refuse a link that stays in.
	t.Run("internal symlink survives and resolves", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "shared/pkg.txt", Typeflag: tar.TypeReg, Size: 4, Mode: 0o644},
			{Name: "app/node_modules", Typeflag: tar.TypeSymlink, Linkname: "../shared", Mode: 0o777},
		}, []string{"deps"})
		if got := put(t, ts.URL+"/stream-in/build-x/out", b); got != 201 {
			t.Fatalf("got %d, want 201 — containment became prohibition", got)
		}
		link := filepath.Join(storagePath, "steps", "build-x", "out", "app", "node_modules")
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("internal symlink lost: err=%v", err)
		}
		// and it still resolves to the shared tree
		got, err := os.ReadFile(filepath.Join(link, "pkg.txt"))
		if err != nil || string(got) != "deps" {
			t.Errorf("link does not resolve inside the artifact: %q err=%v", got, err)
		}
	})

	// A multi-segment key must still work — every real key is one
	// (handle/output), so the parent MkdirAll through the nested root is
	// load-bearing.
	t.Run("multi-segment key still works", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
		}, []string{"ok"})
		if got := put(t, ts.URL+"/stream-in/handle/output", b); got != 201 {
			t.Fatalf("got %d, want 201", got)
		}
		if _, err := os.Stat(filepath.Join(storagePath, "steps", "handle", "output", "f.txt")); err != nil {
			t.Errorf("multi-segment key did not land: %v", err)
		}
	})

	// The boundary binds: a symlink planted under steps/ pointing at the store
	// ROOT must not let a later stream-in escape steps/ — even though the store
	// root is still "inside the daemon's storage".
	t.Run("cannot escape steps into the store root", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		sentinel := filepath.Join(storagePath, "aliases.json")
		if err := os.WriteFile(sentinel, []byte(`{"legit":"data"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		// Planted DIRECTLY on disk, not through the API. Since phase 1 an
		// absolute symlink is refused at ingress, so this link can no longer be
		// created through stream-in — but one may predate this track, or have
		// arrived by another path. The boundary must hold regardless of how the
		// link got there, which is the whole point of it being a handle rather
		// than a check on the way in.
		if err := os.MkdirAll(filepath.Join(storagePath, "steps", "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(storagePath, filepath.Join(storagePath, "steps", "x", "link")); err != nil {
			t.Fatal(err)
		}

		payload := tarOf(t, []*tar.Header{
			{Name: "f", Typeflag: tar.TypeReg, Size: 8, Mode: 0o644},
		}, []string{"POISONED"})
		got := put(t, ts.URL+"/stream-in/x/link/aliases.json", payload)
		if got < 400 {
			t.Errorf("stream-in through a link to the store root -> %d, want 4xx", got)
		}
		if b, err := os.ReadFile(sentinel); err != nil || !bytes.Contains(b, []byte("legit")) {
			t.Errorf("the alias store was damaged: %q err=%v", b, err)
		}
	})

	// The temp directory must not be addressable as an artifact, and must be
	// cleaned up on the refusal path.
	t.Run("no temp directory leaks on refusal", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		bad := tarOf(t, []*tar.Header{
			{Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/tmp-leak/out", bad); got != 400 {
			t.Fatalf("got %d, want 400", got)
		}
		entries, err := os.ReadDir(filepath.Join(storagePath, "steps"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".in-tmp-") {
				t.Errorf("temp directory %q leaked after a refused stream-in", e.Name())
			}
		}
	})
}
