package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseAgentSnapshotIDPreservesExactInt64Identity(t *testing.T) {
	for _, raw := range []string{"1", "9007199254740993", "9223372036854775807"} {
		if got, err := parseAgentSnapshotID(raw); err != nil || got != raw {
			t.Fatalf("parseAgentSnapshotID(%q) = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", " 1", "1 ", "01", "9223372036854775808"} {
		if _, err := parseAgentSnapshotID(raw); err == nil {
			t.Fatalf("parseAgentSnapshotID(%q) succeeded", raw)
		}
	}
}

func TestWriteAgentSnapshotTarIsDeterministicAndNormalized(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "z.txt"), []byte("z"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "run"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("z.txt", filepath.Join(root, "safe-link")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(10, 0)
	newer := time.Unix(2_000_000_000, 0)
	if err := os.Chtimes(filepath.Join(first, "z.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(second, "z.txt"), newer, newer); err != nil {
		t.Fatal(err)
	}

	var one, two bytes.Buffer
	if err := writeAgentSnapshotTar(context.Background(), first, &one); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentSnapshotTar(context.Background(), second, &two); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one.Bytes(), two.Bytes()) {
		t.Fatal("equivalent directory trees produced different tar bytes")
	}

	type entry struct {
		name string
		mode int64
		kind byte
		link string
		body string
	}
	var got []entry
	tr := tar.NewReader(bytes.NewReader(one.Bytes()))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if !hdr.ModTime.Equal(time.Unix(0, 0).UTC()) || hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("non-canonical header for %q: %#v", hdr.Name, hdr)
		}
		got = append(got, entry{hdr.Name, hdr.Mode, hdr.Typeflag, hdr.Linkname, string(body)})
	}
	want := []entry{
		{name: "empty/", mode: 0o755, kind: tar.TypeDir},
		{name: "run", mode: 0o755, kind: tar.TypeReg, body: "#!/bin/sh\n"},
		{name: "safe-link", mode: 0o777, kind: tar.TypeSymlink, link: "z.txt"},
		{name: "z.txt", mode: 0o644, kind: tar.TypeReg, body: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tar entries = %#v, want %#v", got, want)
	}
}

func TestWriteAgentSnapshotTarRejectsUnsafeFilesystemEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only special-file and symlink checks")
	}
	tests := map[string]func(string) error{
		"absolute symlink": func(root string) error { return os.Symlink("/etc/passwd", filepath.Join(root, "bad")) },
		"escaping symlink": func(root string) error { return os.Symlink("../outside", filepath.Join(root, "bad")) },
		"fifo":             func(root string) error { return makeNamedPipe(filepath.Join(root, "bad")) },
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := setup(root); err != nil {
				t.Fatal(err)
			}
			if err := writeAgentSnapshotTar(context.Background(), root, io.Discard); err == nil {
				t.Fatal("unsafe tree was accepted")
			}
		})
	}
	if err := validateAgentSnapshotMode("setuid", 0o755|os.ModeSetuid); err == nil {
		t.Fatal("setuid mode was accepted")
	}
	if err := validateAgentSnapshotMode("setgid", 0o755|os.ModeSetgid); err == nil {
		t.Fatal("setgid mode was accepted")
	}
}

func TestWriteAgentSnapshotTarHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingWriter{cancel: cancel}
	if err := writeAgentSnapshotTar(ctx, root, writer); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeAgentSnapshotTar() error = %v, want context cancellation", err)
	}
}

func TestWriteAgentSnapshotTarKeepsPreflightRootAnchoredAcrossPathSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement and symlink behavior is Unix-specific")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	movedSource := filepath.Join(parent, "source-opened")
	sensitive := filepath.Join(parent, "sensitive")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sensitive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "safe.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensitive, "secret.txt"), []byte("must-not-leak"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := openAgentSnapshotDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(source, movedSource); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sensitive, source); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	if err := writeAgentSnapshotTarFromRoot(context.Background(), root, &archive); err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(archive.Bytes()))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = string(body)
	}
	if got := entries["safe.txt"]; got != "safe" {
		t.Fatalf("anchored source body = %q, want safe", got)
	}
	if _, found := entries["secret.txt"]; found || strings.Contains(archive.String(), "must-not-leak") {
		t.Fatalf("replacement path content leaked into archive: %#v", entries)
	}
}

func TestWriteAgentSnapshotDownloadPublishesOnlyVerifiedContent(t *testing.T) {
	body := []byte("canonical tar bytes")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	destination := filepath.Join(t.TempDir(), "snapshot.tar")
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Length": []string{fmt.Sprint(len(body))},
			"Etag":           []string{`"` + digest + `"`},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
	if err := writeAgentSnapshotDownload(response, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded bytes = %q, want %q", got, body)
	}
}

func TestWriteAgentSnapshotDownloadPreservesDestinationOnFailure(t *testing.T) {
	tests := map[string]func([]byte) *http.Response{
		"short body": func(body []byte) *http.Response {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
				"Content-Length": []string{fmt.Sprint(len(body) + 1)},
				"Etag":           []string{fmt.Sprintf(`"sha256:%x"`, sha256.Sum256(body))},
			}, Body: io.NopCloser(bytes.NewReader(body))}
		},
		"digest mismatch": func(body []byte) *http.Response {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
				"Content-Length": []string{fmt.Sprint(len(body))},
				"Etag":           []string{`"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
			}, Body: io.NopCloser(bytes.NewReader(body))}
		},
		"missing etag": func(body []byte) *http.Response {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
				"Content-Length": []string{fmt.Sprint(len(body))},
			}, Body: io.NopCloser(bytes.NewReader(body))}
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			destination := filepath.Join(directory, "snapshot.tar")
			if err := os.WriteFile(destination, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeAgentSnapshotDownload(response([]byte("partial")), destination); err == nil {
				t.Fatal("invalid download succeeded")
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "original" {
				t.Fatalf("existing destination changed to %q", got)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "snapshot.tar" {
				t.Fatalf("temporary download leaked: %v", entries)
			}
		})
	}
}

func TestWriteAgentSnapshotDownloadClosesBodyOnHeaderFailure(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("body")}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Length": []string{"4"}},
		Body:       body,
	}
	if err := writeAgentSnapshotDownload(response, filepath.Join(t.TempDir(), "snapshot.tar")); err == nil {
		t.Fatal("download without ETag succeeded")
	}
	if body.closed != 1 {
		t.Fatalf("response body close count = %d, want 1", body.closed)
	}
}

func TestCompleteAgentSnapshotCreateDrainsAndClosesSuccessfulResponseBeforeReturningLateTarError(t *testing.T) {
	lateTarError := errors.New("source changed while archiving")
	body := &trackingReadCloser{Reader: strings.NewReader("server response")}
	response := &http.Response{
		StatusCode: http.StatusCreated,
		Status:     "201 Created",
		Body:       body,
	}

	err := completeAgentSnapshotCreate(response, nil, lateTarError, nil)
	if !errors.Is(err, lateTarError) {
		t.Fatalf("completeAgentSnapshotCreate() error = %v, want late tar error", err)
	}
	if body.closed != 1 {
		t.Fatalf("response body close count = %d, want 1", body.closed)
	}
	remaining, readErr := io.ReadAll(body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(remaining) != 0 {
		t.Fatalf("response body was not drained; remaining = %q", remaining)
	}
}

type cancelingWriter struct {
	cancel func()
	wrote  bool
}

type trackingReadCloser struct {
	io.Reader
	closed int
}

func (r *trackingReadCloser) Close() error {
	r.closed++
	return nil
}

func makeNamedPipe(path string) error {
	return syscall.Mkfifo(path, 0o600)
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		w.cancel()
	}
	return len(p), nil
}
