package main_test

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// rootfsTarWithAbsoluteSymlink builds the shape of a container rootfs: a
// regular file plus the absolute symlinks every Debian-derived image carries.
func rootfsTarWithAbsoluteSymlink(t *testing.T) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	write := func(hdr *tar.Header, body string) {
		t.Helper()
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", hdr.Name, err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("write body %q: %v", hdr.Name, err)
			}
		}
	}

	write(&tar.Header{Name: "rootfs/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{Name: "rootfs/marker.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 8}, "rootfs!\n")
	write(&tar.Header{Name: "rootfs/var/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{Name: "rootfs/var/spool/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{
		Name: "rootfs/var/spool/mail", Typeflag: tar.TypeSymlink, Linkname: "/var/mail", Mode: 0o777,
	}, "")
	write(&tar.Header{
		Name: "rootfs/bin/ls", Typeflag: tar.TypeSymlink, Linkname: "/bin/busybox", Mode: 0o777,
	}, "")

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// TestMirror_CarriesAbsoluteSymlinksToPeer covers replication of a container
// rootfs, which no other test reaches.
//
// It exercises both halves of the symlink rule at once: server A must extract
// an archive whose members carry absolute targets, then re-tar that tree to
// mirror it, and server B must extract what A emits. Before the rule was made
// consistent, each half refused those targets independently — so a rootfs could
// be resolved on the node that produced it and never replicated, and A could
// not have re-ingested its own mirror stream.
//
// The k8s behavioural suite cannot cover this: it runs a single-node K3s, so
// there is no peer to mirror to. agentSnapshots.replicationFactor is only an
// upper bound on endpoints chosen, so raising it there would change nothing —
// the limitation is topology, and two in-process daemons are the way to reach
// it.
func TestMirror_CarriesAbsoluteSymlinksToPeer(t *testing.T) {
	storageB := t.TempDir()
	serverB := daemon.NewServer(lagertest.NewTestLogger("server-b"), storageB, "node-b")
	tsB := httptest.NewServer(serverB.Handler())
	defer tsB.Close()
	hostB, portB := splitHostPort(t, tsB.Listener.Addr().String())

	storageA := t.TempDir()
	loggerA := lagertest.NewTestLogger("server-a")
	serverA := daemon.NewServer(loggerA, storageA, "node-a")

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{hostB}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
	})
	peers := daemon.NewPeerResolver(loggerA, clientset, "concourse", "artifact-daemon", portB, "10.0.0.99", nil)

	mirror := daemon.NewMirror(daemon.MirrorConfig{
		StoragePath:    storageA,
		Port:           portB,
		Scheme:         "http",
		Replicas:       2,
		Concurrency:    4,
		PerPeerTimeout: 5 * time.Second,
		Peers:          peers,
		Client:         &http.Client{Timeout: 5 * time.Second},
		Logger:         loggerA,
	})
	defer mirror.Stop()
	serverA.SetMirrorTrigger(mirror.Trigger)

	tsA := httptest.NewServer(serverA.Handler())
	defer tsA.Close()

	req, _ := http.NewRequest(http.MethodPut, tsA.URL+"/stream-in/handle/rootfs", rootfsTarWithAbsoluteSymlink(t))
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /stream-in to A: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stream-in of a rootfs with absolute symlinks: expected 201, got %d", resp.StatusCode)
	}

	// The symlink must arrive on the peer as a symlink with its target intact.
	mirroredLink := filepath.Join(storageB, "steps", "handle/rootfs", "rootfs", "var", "spool", "mail")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(mirroredLink); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	info, err := os.Lstat(mirroredLink)
	if err != nil {
		t.Fatalf("absolute symlink never reached the peer at %s: %v", mirroredLink, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("entry mirrored to the peer is not a symlink — the target was dereferenced in transit")
	}
	target, err := os.Readlink(mirroredLink)
	if err != nil {
		t.Fatalf("readlink on the peer: %v", err)
	}
	if target != "/var/mail" {
		t.Errorf("mirrored symlink target = %q, want %q", target, "/var/mail")
	}

	// Ordinary members must survive alongside it.
	body, err := os.ReadFile(filepath.Join(storageB, "steps", "handle/rootfs", "rootfs", "marker.txt"))
	if err != nil || string(body) != "rootfs!\n" {
		t.Errorf("regular file alongside the symlinks: %q, %v", body, err)
	}
}
