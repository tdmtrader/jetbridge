package main_test

import (
	"archive/tar"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
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
