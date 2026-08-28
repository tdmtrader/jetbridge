package main

// The destination lock must cover EVERY writer of a resolve destination —
// including the peer-fetch branch, which does its own clear-and-rename on the
// same dest without going through copyArtifactGuarded. These are in-package
// because acquireDest is unexported, and holding the lock directly is the only
// deterministic way to prove a branch waits on it.

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// peeredServer returns a daemon whose only peer is a second, seeded daemon, so
// resolveOne's Step 3 is the branch that answers.
func peeredServer(t *testing.T) (*Server, string) {
	t.Helper()

	peerStorage := t.TempDir()
	stepDir := filepath.Join(peerStorage, "steps", "peer-handle", "output")
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "data.txt"), []byte("peer-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	peer := newServerT(t, lagertest.NewTestLogger("peer"), peerStorage, "peer-node")
	tsPeer := httptest.NewServer(peer.Handler())
	t.Cleanup(tsPeer.Close)

	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(tsPeer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

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

	storage := t.TempDir()
	logger := lagertest.NewTestLogger("local")
	s := newServerT(t, logger, storage, "local-node")
	s.SetPeerResolver(NewPeerResolver(logger, clientset, "concourse", "artifact-daemon", port, "10.0.0.99", nil))
	return s, storage
}

func TestResolveOne_PeerFetchWaitsForTheDestinationLock(t *testing.T) {
	s, storage := peeredServer(t)
	dest := filepath.Join(storage, "resolved", "in")

	// Hold the destination. A peer-backed resolve of that dest must now wait,
	// and give up when its request context dies — not fetch around the lock.
	release, err := s.acquireDest(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp := s.resolveOne(ctx, "peer-handle/output", dest)
	if resp.Status != "error" {
		t.Fatalf("peer fetch proceeded while the destination was locked: status=%q method=%q", resp.Status, resp.Method)
	}
	if !strings.Contains(resp.Error, "context deadline exceeded") {
		t.Errorf("expected a lock-wait timeout, got: %s", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(dest, "data.txt")); !os.IsNotExist(err) {
		t.Errorf("bytes landed at a locked destination (err=%v)", err)
	}

	// Released: the identical resolve succeeds via the peer.
	release()
	resp = s.resolveOne(context.Background(), "peer-handle/output", dest)
	if resp.Status != "ok" || resp.Method != "peer" {
		t.Fatalf("after release: status=%q method=%q error=%q", resp.Status, resp.Method, resp.Error)
	}
	got, err := os.ReadFile(filepath.Join(dest, "data.txt"))
	if err != nil || string(got) != "peer-bytes" {
		t.Errorf("peer artifact not delivered: %q err=%v", got, err)
	}
}

// A peer fetch that cannot clear the destination must say so. The old path
// ignored the RemoveAll error, hit ENOTEMPTY on the rename, and reported the
// whole fetch as success — delivering whatever was already sitting at dest.
func TestPeerFetch_FailedClearIsAnErrorNotSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write bits; the failed clear cannot be staged")
	}
	s, storage := peeredServer(t)
	dest := filepath.Join(storage, "resolved", "in")

	// Stage a destination that RemoveAll cannot clear: a read-only directory
	// with a child. Removing the child needs write permission on blocker.
	blocker := filepath.Join(dest, "blocker")
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(blocker, "stale.txt")
	if err := os.WriteFile(stale, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocker, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocker, 0o755) })

	resp := s.resolveOne(context.Background(), "peer-handle/output", dest)
	if resp.Status == "ok" {
		t.Fatalf("fetch reported success while the stale destination could not be cleared")
	}
	if b, err := os.ReadFile(stale); err != nil || string(b) != "STALE" {
		t.Errorf("expected the stale content to still be present (delivered nothing), got %q err=%v", b, err)
	}
}
