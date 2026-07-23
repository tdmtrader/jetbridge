package main_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/artifactcap"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestResolveBatchMaterializesDigestVerifiedSnapshot(t *testing.T) {
	storage := t.TempDir()
	archive := snapshotArchive(t, "repo/file.txt", "immutable repository state")
	digest := snapshotArchiveDigest(archive)
	writeSnapshotArchive(t, storage, digest, archive)

	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := artifactcap.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	server := daemon.NewServer(lagertest.NewTestLogger("snapshot-resolve"), storage, "node-a")
	httpServer := httptest.NewServer(server.Handler(daemon.WithResolveCapabilityKey(key)))
	defer httpServer.Close()

	destination := filepath.Join(storage, "steps", "consumer", "repository")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	mountedDestination, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer mountedDestination.Close()

	snapshotKey := "snapshots/sha256/" + digest + ".tar"
	capability, err := signer.SignResolve(snapshotKey, destination, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	response := postSnapshotResolveBatch(t, httpServer, snapshotKey, destination, capability)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("snapshot resolve status = %d: %s", response.StatusCode, body)
	}
	var result struct {
		Status  string            `json:"status"`
		Results []resolveResponse `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != "ok" || result.Results[0].Method != "snapshot-local" {
		t.Fatalf("snapshot resolve response = %+v", result)
	}

	materialized, err := mountedDestination.Open("repo/file.txt")
	if err != nil {
		t.Fatalf("bind-mounted destination did not observe snapshot: %v", err)
	}
	contents, readErr := io.ReadAll(materialized)
	materialized.Close()
	if readErr != nil || string(contents) != "immutable repository state" {
		t.Fatalf("materialized snapshot = %q, %v", contents, readErr)
	}

	acknowledgement, err := signer.ResolveAcknowledgement(capability)
	if err != nil {
		t.Fatal(err)
	}
	receiptName, err := artifactcap.ResolveReceiptFilename(acknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := mountedDestination.Open(receiptName)
	if err != nil {
		t.Fatalf("snapshot resolve receipt is not readable by the helper: %v", err)
	}
	receiptContents, readErr := io.ReadAll(receipt)
	receiptInfo, statErr := receipt.Stat()
	receipt.Close()
	if readErr != nil || statErr != nil || string(receiptContents) != acknowledgement {
		t.Fatalf("snapshot receipt = %q/%v/%v", receiptContents, readErr, statErr)
	}
	if receiptInfo.Mode().Perm()&0044 != 0044 {
		t.Fatalf("snapshot receipt mode = %o, want non-root readable", receiptInfo.Mode().Perm())
	}
}

func TestResolveBatchRejectsCorruptSnapshotBeforePublishing(t *testing.T) {
	storage := t.TempDir()
	wantArchive := snapshotArchive(t, "safe.txt", "expected")
	digest := snapshotArchiveDigest(wantArchive)
	writeSnapshotArchive(t, storage, digest, snapshotArchive(t, "unsafe.txt", "corrupt"))

	key := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := artifactcap.NewSigner(key)
	server := daemon.NewServer(lagertest.NewTestLogger("corrupt-snapshot-resolve"), storage, "node-a")
	httpServer := httptest.NewServer(server.Handler(daemon.WithResolveCapabilityKey(key)))
	defer httpServer.Close()

	destination := filepath.Join(storage, "steps", "consumer", "repository")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "existing.txt"), []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshotKey := "snapshots/sha256/" + digest + ".tar"
	capability, _ := signer.SignResolve(snapshotKey, destination, time.Now().Add(time.Minute))
	response := postSnapshotResolveBatch(t, httpServer, snapshotKey, destination, capability)
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("corrupt snapshot status = %d, want 500: %s", response.StatusCode, body)
	}
	if contents, err := os.ReadFile(filepath.Join(destination, "existing.txt")); err != nil || string(contents) != "preserve" {
		t.Fatalf("failed resolve changed existing destination: %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "unsafe.txt")); !os.IsNotExist(err) {
		t.Fatalf("corrupt snapshot published partial contents: %v", err)
	}
}

func TestResolveBatchFetchesSnapshotFromPeer(t *testing.T) {
	archive := snapshotArchive(t, "peer.txt", "replicated snapshot")
	digest := snapshotArchiveDigest(archive)

	peerStorage := t.TempDir()
	writeSnapshotArchive(t, peerStorage, digest, archive)
	peerServer := daemon.NewServer(lagertest.NewTestLogger("snapshot-peer"), peerStorage, "node-a")
	peerHTTP := httptest.NewServer(peerServer.Handler())
	defer peerHTTP.Close()
	peerHost, peerPort := splitHostPort(t, peerHTTP.Listener.Addr().String())

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{peerHost},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	})

	localStorage := t.TempDir()
	logger := lagertest.NewTestLogger("snapshot-local")
	localServer := daemon.NewServer(logger, localStorage, "node-b")
	localServer.SetPeerResolver(daemon.NewPeerResolver(
		logger, clientset, "concourse", "artifact-daemon", peerPort, "10.0.0.99", nil,
	))
	capabilityKey := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := artifactcap.NewSigner(capabilityKey)
	localHTTP := httptest.NewServer(localServer.Handler(daemon.WithResolveCapabilityKey(capabilityKey)))
	defer localHTTP.Close()

	destination := filepath.Join(localStorage, "steps", "consumer", "repository")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	snapshotKey := "snapshots/sha256/" + digest + ".tar"
	capability, _ := signer.SignResolve(snapshotKey, destination, time.Now().Add(time.Minute))
	response := postSnapshotResolveBatch(t, localHTTP, snapshotKey, destination, capability)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("peer snapshot resolve status = %d: %s", response.StatusCode, body)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "peer.txt"))
	if err != nil || string(contents) != "replicated snapshot" {
		t.Fatalf("peer snapshot contents = %q, %v", contents, err)
	}
}

func postSnapshotResolveBatch(
	t *testing.T,
	server *httptest.Server,
	key string,
	destination string,
	capability string,
) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"items": []map[string]string{{
		"key": key, "dest": destination, "capability": capability,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return boundaryRequest(t, server.Client(), http.MethodPost, server.URL+"/resolve-batch", bytes.NewReader(body))
}

func snapshotArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		t.Fatalf("unsafe test archive name %q", name)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	payload := []byte(contents)
	if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func snapshotArchiveDigest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

func writeSnapshotArchive(t *testing.T, storage, digest string, archive []byte) {
	t.Helper()
	directory := filepath.Join(storage, "snapshots", "sha256")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, digest+".tar"), archive, 0644); err != nil {
		t.Fatal(err)
	}
}
