package atccmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactResolveCapabilityKeyIsLoadedOncePerCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolve.key")
	first := []byte("0123456789abcdef0123456789abcdef")
	second := []byte("fedcba9876543210fedcba9876543210")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}

	command := &RunCommand{}
	command.Kubernetes.ArtifactDaemonResolveCapabilityKey = path
	loaded, err := command.loadArtifactResolveCapabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	loaded[0] ^= 0xff
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}

	again, err := command.loadArtifactResolveCapabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, first) {
		t.Fatalf("cached key = %q, want first immutable startup value", again)
	}
}
