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

func assertDaemonPayload(t *testing.T, packed []byte, digest string, want []byte) {
	t.Helper()
	stream, err := gzip.NewReader(bytes.NewReader(packed))
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

func openDaemonFixture(t *testing.T, contents []byte) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return path, file
}

func TestLiveDaemonPayloadIsReusedForTheSameBytes(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 'x', '\n'}, 4096)
	_, file := openDaemonFixture(t, data)
	var cache liveDaemonPayloadCache
	first, digest, err := cache.payload(file)
	if err != nil {
		t.Fatal(err)
	}
	second, sameDigest, err := cache.payload(file)
	if err != nil {
		t.Fatal(err)
	}
	if &first[0] != &second[0] {
		t.Fatal("the same executable was compressed twice")
	}
	assertDaemonPayload(t, first, digest, data)
	assertDaemonPayload(t, second, sameDigest, data)
}

func TestLiveDaemonPayloadRechecksActualFileContents(t *testing.T) {
	first := bytes.Repeat([]byte{0, 255, 'a', '\n'}, 4096)
	second := bytes.Repeat([]byte{0, 254, 'b', '\n'}, 4096)
	path, file := openDaemonFixture(t, first)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var cache liveDaemonPayloadCache
	old, oldDigest, err := cache.payload(file)
	if err != nil {
		t.Fatal(err)
	}
	// Neither the path, size nor modification time can stand in for bytes.
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated, updatedDigest, err := cache.payload(file)
	if err != nil {
		t.Fatal(err)
	}
	assertDaemonPayload(t, updated, updatedDigest, second)
	// Replacing the cached payload must not change one handed out earlier.
	assertDaemonPayload(t, old, oldDigest, first)
}

func TestLiveDaemonPayloadDoesNotHideUnreadableInput(t *testing.T) {
	_, file := openDaemonFixture(t, []byte("executable bytes"))
	var cache liveDaemonPayloadCache
	if _, _, err := cache.payload(file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.payload(file); err == nil {
		t.Fatal("a cached payload hid a real file read failure")
	}
}
