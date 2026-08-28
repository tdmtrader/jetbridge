package main_test

// The delivered artifact DIRECTORY must be traversable by an arbitrary UID.
//
// dest is a hostPath volume mounted into the task container, and task images
// choose their own USER — buildPodSecurityContext deliberately sets no
// runAsUser. A 0700 root-owned destination therefore fails the step with
// EACCES before a single file is read, and every permissive mode underneath is
// unreachable. The daemon's own staging dirs are created 0700 and promoted by
// rename (which preserves the mode), so this has to be asserted on the
// promoted directory itself, on every path that promotes one: local copy,
// peer fetch, and durable restore.

import (
	"archive/tar"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// wantTraversable fails unless dir is readable and traversable by others.
func wantTraversable(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm&0o055 != 0o055 {
		t.Errorf("destination %s is mode %04o; a non-root task container cannot read its input (want at least o+rx)", dir, perm)
	}
}

func TestResolve_LocalCopyLeavesATraversableDestination(t *testing.T) {
	ts, storagePath := setupServer(t)

	// A producing step that wrote everything 0700/0600 — the case the mode
	// floor exists for.
	srcDir := filepath.Join(storagePath, "steps", "perm-handle", "out")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "f.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	destDir := destUnder(t, storagePath, "perm-dest")
	body := `{"key":"perm-handle/out","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}

	wantTraversable(t, destDir)
}

func TestResolve_BatchLeavesATraversableDestination(t *testing.T) {
	ts, storagePath := setupServer(t)

	srcDir := filepath.Join(storagePath, "steps", "batch-perm", "out")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "f.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	destDir := destUnder(t, storagePath, "batch-perm-dest")
	body, _ := json.Marshal(batchRequest{Items: []batchItem{{Key: "batch-perm/out", Dest: destDir}}})
	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}

	wantTraversable(t, destDir)
}

// The peer path promotes a directory extracted from a tar, so it has its own
// staging dir and its own chance to deliver an unreadable mount.
func TestResolve_PeerFetchLeavesATraversableDestination(t *testing.T) {
	// Peer A holds the artifact.
	storageA := t.TempDir()
	stepDir := filepath.Join(storageA, "steps", "peer-perm", "out")
	if err := os.MkdirAll(stepDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "f.txt"), []byte("peer"), 0o600); err != nil {
		t.Fatal(err)
	}
	loggerA := lagertest.NewTestLogger("peer-a")
	serverA := newDaemonServer(t, loggerA, storageA, "node-a")
	tsA := httptest.NewServer(serverA.Handler())
	defer tsA.Close()
	hostA, portA := splitHostPort(t, tsA.Listener.Addr().String())

	// Peer B has nothing and must fetch.
	storageB := t.TempDir()
	loggerB := lagertest.NewTestLogger("peer-b")
	serverB := newDaemonServer(t, loggerB, storageB, "node-b")

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{hostA}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
	})
	serverB.SetPeerResolver(daemon.NewPeerResolver(loggerB, clientset, "concourse", "artifact-daemon", portA, "10.0.0.99", nil))

	tsB := httptest.NewServer(serverB.Handler())
	defer tsB.Close()

	destDir := destUnder(t, storageB, "peer-perm-dest")
	body := `{"key":"peer-perm/out","dest":"` + destDir + `"}`
	resp, err := http.Post(tsB.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}

	var result resolveResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Method != "peer" {
		t.Fatalf("method = %q, want peer — the local path answered and this proves nothing", result.Method)
	}

	wantTraversable(t, destDir)
	if _, err := os.Stat(filepath.Join(destDir, "f.txt")); err != nil {
		t.Errorf("peer artifact not delivered: %v", err)
	}
}

// Stream-in extracts into its own staging dir under steps/ and promotes it.
// That directory is later the SOURCE of a resolve, and the sweeper and the tar
// producer walk it, so it must not be 0700 either.
func TestStreamIn_LeavesATraversableArtifactDirectory(t *testing.T) {
	ts, storagePath := setupServer(t)

	b := tarOf(t, []*tar.Header{
		{Name: "f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o600},
	}, []string{"ok"})
	if got := put(t, ts.URL+"/stream-in/perm-in/out", b); got != 201 {
		t.Fatalf("got %d, want 201", got)
	}

	wantTraversable(t, filepath.Join(storagePath, "steps", "perm-in", "out"))
}
