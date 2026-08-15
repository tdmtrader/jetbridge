package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// Symlinks are part of task output semantics, not metadata that mirroring may
// flatten. Exercise the sender's tar stream and the peer's real /stream-in
// extraction together so both the link target and link type survive transit.
func TestMirrorJobRoundTripsSymlink(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "target.txt"), []byte("linked payload"), 0o644); err != nil {
		t.Fatalf("write mirror target: %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(source, "link.txt")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	peerStorage := t.TempDir()
	peer := NewServer(lagertest.NewTestLogger("symlink-peer"), peerStorage, "peer")
	peerHTTP := httptest.NewServer(peer.Handler())
	t.Cleanup(peerHTTP.Close)

	const peerHost = "symlink-peer"
	job := &mirrorJob{
		key:            "handle/output",
		sourceDir:      source,
		peers:          []string{peerHost},
		port:           7780,
		scheme:         "http",
		client:         &http.Client{Transport: &mirrorRoutingTransport{routes: map[string]string{peerHost + ":7780": peerHTTP.URL}}},
		logger:         lagertest.NewTestLogger("symlink-mirror"),
		perPeerTimeout: time.Second,
	}
	outcomes := job.Run(context.Background())
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("symlink mirror outcome = %+v, want one ok", outcomes)
	}

	link := filepath.Join(peerStorage, "steps", "handle", "output", "link.txt")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat mirrored link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mirrored link mode = %v, want symlink", info.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("read mirrored link: %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("mirrored link target = %q, want target.txt", target)
	}
	payload, err := os.ReadFile(link)
	if err != nil {
		t.Fatalf("follow mirrored link: %v", err)
	}
	if string(payload) != "linked payload" {
		t.Fatalf("mirrored link payload = %q", payload)
	}
}
