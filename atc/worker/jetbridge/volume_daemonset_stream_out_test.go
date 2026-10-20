package jetbridge

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/concourse/concourse/atc/compression"
)

// daemonShapedTar is the tar the artifact daemon serves for an artifact key:
// entry names relative to the key, no entry for the root itself, no leading "/"
// or "./" (cmd/artifact-daemon tarTree).
func daemonShapedTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []struct {
		name, body string
		dir        bool
	}{
		{name: "sub", dir: true},
		{name: "sub/file", body: "nested"},
		{name: "top.txt", body: "top"},
	} {
		hdr := &tar.Header{Name: e.name, Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			hdr = &tar.Header{Name: e.name, Mode: 0755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatalf("write body %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// streamOutEntries streams path out of a DaemonSetVolume whose artifact daemon serves
// daemonShapedTar, gzip-compressed the way the artifact server asks for it,
// and returns the entry names and bodies of the archive it gets back.
func streamOutEntries(t *testing.T, path string) ([]string, map[string]string) {
	t.Helper()
	archive := daemonShapedTar(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	vol := NewDaemonSetVolumeFromIP("h/o", "h", "w", "10.0.0.1", Config{})
	vol.wire = plainWire(t, 7780, rewriteTransport{url: srv.URL})

	gz := compression.NewGzipCompression()
	out, err := vol.StreamOut(context.Background(), path, gz)
	if err != nil {
		t.Fatalf("StreamOut(%q): %v", path, err)
	}
	defer out.Close()

	raw, err := gz.NewReader(out)
	if err != nil {
		t.Fatalf("gunzip StreamOut(%q): %v", path, err)
	}
	defer raw.Close()

	var names []string
	bodies := map[string]string{}
	tr := tar.NewReader(raw)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive of StreamOut(%q): %v", path, err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		names = append(names, hdr.Name)
		bodies[hdr.Name] = string(body)
	}
	return names, bodies
}

// The artifact server streams every download (`fly execute -o`) with "/".
// That is the whole volume, not a sub-path no tar entry is named; before
// this, the download was a gzip of an empty tar and fly reported success.
func TestDaemonSetVolume_StreamOut_RootSlashIsTheWholeVolume(t *testing.T) {
	want := []string{"sub", "sub/file", "top.txt"}
	for _, path := range []string{"/", ".", "", "./"} {
		names, bodies := streamOutEntries(t, path)
		if !slices.Equal(names, want) {
			t.Errorf("StreamOut(%q) entries = %q, want %q", path, names, want)
			continue
		}
		if bodies["sub/file"] != "nested" || bodies["top.txt"] != "top" {
			t.Errorf("StreamOut(%q) bodies = %q", path, bodies)
		}
	}
}

// A sub-path written from the volume root ("/top.txt", "./top.txt") names the
// same entry as "top.txt".
func TestDaemonSetVolume_StreamOut_RootedSubPathNamesTheSameEntry(t *testing.T) {
	for _, path := range []string{"top.txt", "/top.txt", "./top.txt"} {
		names, bodies := streamOutEntries(t, path)
		if !slices.Equal(names, []string{"top.txt"}) || bodies["top.txt"] != "top" {
			t.Errorf("StreamOut(%q) = %q %q, want only top.txt with body \"top\"", path, names, bodies)
		}
	}
}
