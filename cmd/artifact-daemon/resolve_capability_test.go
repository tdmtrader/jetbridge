package main_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/artifactcap"
	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestResolveCapabilitiesAreRequiredAndBoundToTheExactOperation(t *testing.T) {
	storagePath := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := artifactcap.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	server := daemon.NewServer(lagertest.NewTestLogger("capability"), storagePath, "node")
	ts := httptest.NewServer(server.Handler(daemon.WithResolveCapabilityKey(key)))
	defer ts.Close()

	sourceKey := "producer/output"
	source := filepath.Join(storagePath, "steps", filepath.FromSlash(sourceKey))
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "secret"), []byte("only-for-consumer"), 0644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(storagePath, "steps", "consumer", "input")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	// Kubelet bind-mounts this already-created hostPath before the init
	// container starts. Keep a descriptor to that exact inode so this test
	// catches implementations that publish by replacing the directory name:
	// such a replacement looks correct from the host but leaves the pod's
	// bind mount pointed at stale contents.
	destinationMount, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer destinationMount.Close()
	otherDestination := filepath.Join(storagePath, "steps", "attacker", "input")
	valid, err := signer.SignResolve(sourceKey, destination, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := signer.SignResolve(sourceKey, destination, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string]map[string]string{
		"unsigned":          {"key": sourceKey, "dest": destination},
		"cross destination": {"key": sourceKey, "dest": otherDestination, "capability": valid},
		"changed source":    {"key": "other/output", "dest": destination, "capability": valid},
		"expired":           {"key": sourceKey, "dest": destination, "capability": expired},
		"tampered":          {"key": sourceKey, "dest": destination, "capability": valid + "x"},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(body)
			resp := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve", bytes.NewReader(encoded))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			responseBody, _ := io.ReadAll(resp.Body)
			if bytes.Contains(responseBody, []byte("acknowledgement")) {
				t.Fatalf("unauthorized request received an acknowledgement: %s", responseBody)
			}
		})
	}
	if _, err := os.Stat(otherDestination); !os.IsNotExist(err) {
		t.Fatalf("unauthorized request mutated destination: %v", err)
	}

	encoded, _ := json.Marshal(map[string]string{"key": sourceKey, "dest": destination, "capability": valid})
	resp := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve", bytes.NewReader(encoded))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("valid capability status = %d: %s", resp.StatusCode, body)
	}
	var resolved struct {
		Acknowledgement string `json:"acknowledgement"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	wantAcknowledgement, err := signer.ResolveAcknowledgement(valid)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Acknowledgement != wantAcknowledgement {
		t.Fatalf("resolve acknowledgement = %q, want authenticated %q", resolved.Acknowledgement, wantAcknowledgement)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "secret")); err != nil || string(got) != "only-for-consumer" {
		t.Fatalf("authorized copy = %q, %v", got, err)
	}
	mountedSecret, err := destinationMount.Open("secret")
	if err != nil {
		t.Fatalf("bind-mounted destination did not observe copied content: %v", err)
	}
	gotMounted, readErr := io.ReadAll(mountedSecret)
	mountedSecret.Close()
	if readErr != nil || string(gotMounted) != "only-for-consumer" {
		t.Fatalf("bind-mounted authorized copy = %q, %v", gotMounted, readErr)
	}
	receiptName, err := artifactcap.ResolveReceiptFilename(wantAcknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := destinationMount.Open(receiptName)
	if err != nil {
		t.Fatalf("successful resolve did not materialize a receipt in the local destination mount: %v", err)
	}
	receiptContents, readErr := io.ReadAll(receipt)
	receipt.Close()
	if readErr != nil || string(receiptContents) != wantAcknowledgement {
		t.Fatalf("local resolve receipt = %q, %v; want exact acknowledgement", receiptContents, readErr)
	}

	missingDest := filepath.Join(storagePath, "steps", "consumer", "missing")
	missingToken, _ := signer.SignResolve("missing/output", missingDest, time.Now().Add(time.Minute))
	missingBody, _ := json.Marshal(map[string]string{"key": "missing/output", "dest": missingDest, "capability": missingToken})
	missingResponse := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve", bytes.NewReader(missingBody))
	if missingResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("missing resolve status = %d, want 404", missingResponse.StatusCode)
	}
	missingResponseBody, _ := io.ReadAll(missingResponse.Body)
	if bytes.Contains(missingResponseBody, []byte("acknowledgement")) {
		t.Fatalf("failed resolve received a success acknowledgement: %s", missingResponseBody)
	}
}

func TestResolveBatchAuthorizesEveryItemBeforeAnyMutation(t *testing.T) {
	storagePath := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, _ := artifactcap.NewSigner(key)
	server := daemon.NewServer(lagertest.NewTestLogger("batch-capability"), storagePath, "node")
	ts := httptest.NewServer(server.Handler(daemon.WithResolveCapabilityKey(key)))
	defer ts.Close()

	sourceKey := "producer/output"
	source := filepath.Join(storagePath, "steps", filepath.FromSlash(sourceKey))
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(storagePath, "steps", "consumer", "first")
	second := filepath.Join(storagePath, "steps", "consumer", "second")
	firstToken, _ := signer.SignResolve(sourceKey, first, time.Now().Add(time.Minute))
	body, _ := json.Marshal(map[string]any{"items": []map[string]string{
		{"key": sourceKey, "dest": first, "capability": firstToken},
		{"key": sourceKey, "dest": second},
	}})
	resp := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve-batch", bytes.NewReader(body))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("batch mutated authorized item before rejecting unsigned sibling: %v", err)
	}

	secondToken, _ := signer.SignResolve(sourceKey, second, time.Now().Add(time.Minute))
	body, _ = json.Marshal(map[string]any{"items": []map[string]string{
		{"key": sourceKey, "dest": first, "capability": firstToken},
		{"key": sourceKey, "dest": second, "capability": secondToken},
	}})
	resp = boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve-batch", bytes.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorized batch status = %d: %s", resp.StatusCode, responseBody)
	}
	var resolved struct {
		Results []struct {
			Acknowledgement string `json:"acknowledgement"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	wantFirst, _ := signer.ResolveAcknowledgement(firstToken)
	wantSecond, _ := signer.ResolveAcknowledgement(secondToken)
	if len(resolved.Results) != 2 || resolved.Results[0].Acknowledgement != wantFirst || resolved.Results[1].Acknowledgement != wantSecond {
		t.Fatalf("batch acknowledgements = %+v, want exact per-item receipts", resolved.Results)
	}
}
