package steps

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func assertDaemonPayload(t *testing.T, reader io.Reader, digest string, want []byte) {
	t.Helper()
	stream, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || digest != fmt.Sprintf("%x", sha256.Sum256(want)) {
		t.Fatalf("decompressed bytes or executable digest changed: bytes=%d want=%d digest=%s", len(got), len(want), digest)
	}
}

func TestLiveDaemonPayloadRechecksActualFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon")
	first := bytes.Repeat([]byte{0, 255, 'a', '\n'}, 4096)
	second := bytes.Repeat([]byte{0, 254, 'b', '\n'}, 4096)
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cache liveDaemonPayloadCache
	old, oldDigest, err := cache.reader(file)
	if err != nil {
		t.Fatal(err)
	}
	// Neither the path, size nor modification timestamp can stand in for bytes.
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated, updatedDigest, err := cache.reader(file)
	if err != nil {
		t.Fatal(err)
	}
	assertDaemonPayload(t, updated, updatedDigest, second)
	// Replacing the single cached payload must not mutate an earlier reader.
	assertDaemonPayload(t, old, oldDigest, first)
}

func TestLiveDaemonPayloadReadersHaveIndependentOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon")
	data := bytes.Repeat([]byte{0, 255, 'x', '\n'}, 4096)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cache liveDaemonPayloadCache
	first, digest, err := cache.reader(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Seek(7, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	second, sameDigest, err := cache.reader(file)
	if err != nil {
		t.Fatal(err)
	}
	assertDaemonPayload(t, second, sameDigest, data)
	if position, err := first.Seek(0, io.SeekCurrent); err != nil || position != 7 {
		t.Fatalf("another upload moved the existing reader: position=%d err=%v", position, err)
	}
	if _, err := first.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	assertDaemonPayload(t, first, digest, data)
}

func TestLiveDaemonPayloadDoesNotHideUnreadableInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon")
	if err := os.WriteFile(path, []byte("executable bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var cache liveDaemonPayloadCache
	if _, _, err := cache.reader(file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.reader(file); err == nil {
		t.Fatal("a cached payload hid a real file read failure")
	}
}
