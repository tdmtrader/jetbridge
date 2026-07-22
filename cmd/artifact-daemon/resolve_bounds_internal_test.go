package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveControlBodiesAndBatchCardinalityAreBounded(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("bounds"), t.TempDir(), "node")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	oversized := bytes.Repeat([]byte(" "), 65<<10)
	resp, err := ts.Client().Post(ts.URL+"/resolve", "application/json", bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", resp.StatusCode)
	}

	items := make([]resolveRequest, 65)
	for i := range items {
		items[i] = resolveRequest{Key: "producer/output", Dest: filepath.Join(server.storagePath, "steps", "consumer", string(rune('a'+i)))}
	}
	body, _ := json.Marshal(batchResolveRequest{Items: items})
	resp, err = ts.Client().Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized batch status = %d, want 413", resp.StatusCode)
	}
}

func TestResolveResponseDoesNotExposeHostFilesystemPaths(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("response-boundary"), storage, "node")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: filepath.Join(storage, "steps", "consumer", "input")})
	resp, err := ts.Client().Post(ts.URL+"/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody := new(bytes.Buffer)
	if _, err := responseBody.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(responseBody.String(), storage) {
		t.Fatalf("resolve response exposed storage path: %s", responseBody.String())
	}
}

func TestProtectedControlBodiesAreBoundedAndStrict(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("protected-bounds"), t.TempDir(), "node")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	for _, endpoint := range []string{"/register", "/mirror"} {
		t.Run(endpoint+" oversized", func(t *testing.T) {
			resp, err := ts.Client().Post(ts.URL+endpoint, "application/json", bytes.NewReader(bytes.Repeat([]byte(" "), 65<<10)))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", resp.StatusCode)
			}
		})
		t.Run(endpoint+" trailing value", func(t *testing.T) {
			resp, err := ts.Client().Post(ts.URL+endpoint, "application/json", bytes.NewBufferString(`{} {}`))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestResolveBatchRejectsDuplicatesBeforeMutation(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(storage, "steps", "consumer", "input")
	server := NewServer(lagertest.NewTestLogger("duplicates"), storage, "node")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	body, _ := json.Marshal(batchResolveRequest{Items: []resolveRequest{{Key: "producer/output", Dest: dest}, {Key: "producer/output", Dest: dest}}})
	resp, err := ts.Client().Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate batch status = %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("duplicate batch mutated destination: %v", err)
	}
}

func TestResolveBatchUsesBoundedWorkers(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("workers"), storage, "node")
	started := make(chan struct{}, 32)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	var active atomic.Int32
	server.copyHooks.sourceOpened = func() {
		active.Add(1)
		started <- struct{}{}
		<-release
		active.Add(-1)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	items := make([]resolveRequest, 16)
	for i := range items {
		items[i] = resolveRequest{Key: "producer/output", Dest: filepath.Join(storage, "steps", "consumer-"+string(rune('a'+i)), "input")}
	}
	body, _ := json.Marshal(batchResolveRequest{Items: items})
	done := make(chan *http.Response, 1)
	go func() {
		resp, _ := ts.Client().Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
		done <- resp
	}()
	for range 8 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("eight bounded workers did not start")
		}
	}
	select {
	case <-started:
		releaseAll()
		t.Fatal("batch started more than eight concurrent resolves")
	case <-time.After(100 * time.Millisecond):
	}
	if got := active.Load(); got != 8 {
		t.Fatalf("active workers = %d, want 8", got)
	}
	releaseAll()
	resp := <-done
	if resp == nil {
		t.Fatal("batch request failed")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d, want 200", resp.StatusCode)
	}
}

func TestResolveAdmissionIsDaemonWide(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("global-admission"), storage, "node")
	if err := server.ConfigureResolveLimits(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server.copyHooks.sourceOpened = func() {
		once.Do(func() { close(started) })
		<-release
	}

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: filepath.Join(storage, "steps", "consumer", "first")})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
		firstDone <- recorder
	}()
	<-started

	body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: filepath.Join(storage, "steps", "consumer", "second")})
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
	if second.Code != http.StatusServiceUnavailable {
		close(release)
		t.Fatalf("excess resolve status = %d, want 503", second.Code)
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("admitted resolve status = %d, want 200", first.Code)
	}
}

func TestResolveOperationDeadlineCancelsLocalCopy(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("resolve-deadline"), storage, "node")
	if err := server.ConfigureResolveLimits(1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	server.copyHooks.sourceOpened = func() { time.Sleep(30 * time.Millisecond) }
	dest := filepath.Join(storage, "steps", "consumer", "input")
	body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: dest})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("timed-out resolve status = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("timed-out resolve published a destination: %v", err)
	}
}

func TestResolveOperationDeadlineCancelsGuardWaitAndReleasesAdmissionSlot(t *testing.T) {
	storage := t.TempDir()
	for _, key := range []string{"blocked/output", "available/output"} {
		source := filepath.Join(storage, "steps", filepath.FromSlash(key))
		if err := os.MkdirAll(source, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "data"), []byte(key), 0644); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(lagertest.NewTestLogger("guard-deadline"), storage, "node")
	if err := server.ConfigureResolveLimits(1, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Simulate a mirror/sweeper holding the destructive side of the source
	// guard. The resolve deadline must include this wait; otherwise all global
	// admission slots can remain occupied indefinitely behind these locks.
	release := server.Guard().BeginSweep("blocked")
	defer release()
	blockedBody, _ := json.Marshal(resolveRequest{
		Key:  "blocked/output",
		Dest: filepath.Join(storage, "steps", "consumer", "blocked"),
	})
	started := time.Now()
	blocked := httptest.NewRecorder()
	server.Handler().ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(blockedBody)))
	if blocked.Code != http.StatusGatewayTimeout {
		t.Fatalf("guard-blocked resolve status = %d, want 504: %s", blocked.Code, blocked.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("guard-blocked resolve ignored its deadline for %s", elapsed)
	}

	// Keep the conflicting lock held. A different resolve must still be
	// admitted, proving the timed-out request released the daemon-wide slot.
	availableBody, _ := json.Marshal(resolveRequest{
		Key:  "available/output",
		Dest: filepath.Join(storage, "steps", "consumer", "available"),
	})
	available := httptest.NewRecorder()
	server.Handler().ServeHTTP(available, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(availableBody)))
	if available.Code != http.StatusOK {
		t.Fatalf("resolve after guard timeout status = %d, want 200: %s", available.Code, available.Body.String())
	}
}

func TestResolveOperationDeadlineCancelsDestinationGuardWaitAndReleasesAdmissionSlot(t *testing.T) {
	storage := t.TempDir()
	for _, key := range []string{"producer/output", "available/output"} {
		source := filepath.Join(storage, "steps", filepath.FromSlash(key))
		if err := os.MkdirAll(source, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "data"), []byte(key), 0644); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(storage, "steps", "consumer", "blocked")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("destination-guard-deadline"), storage, "node")
	if err := server.ConfigureResolveLimits(1, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Model an in-flight GET/mirror read of the exact destination handle. An
	// in-place resolve publication is destructive and must wait for this read,
	// with that wait included in the resolve deadline.
	release := server.Guard().BeginRead("consumer")
	defer release()
	body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: destination})
	blocked := httptest.NewRecorder()
	started := time.Now()
	server.Handler().ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
	if blocked.Code != http.StatusGatewayTimeout {
		t.Fatalf("destination-guard-blocked resolve status = %d, want 504: %s", blocked.Code, blocked.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("destination guard wait ignored resolve deadline for %s", elapsed)
	}

	availableBody, _ := json.Marshal(resolveRequest{
		Key:  "available/output",
		Dest: filepath.Join(storage, "steps", "other-consumer", "available"),
	})
	available := httptest.NewRecorder()
	server.Handler().ServeHTTP(available, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(availableBody)))
	if available.Code != http.StatusOK {
		t.Fatalf("resolve after destination guard timeout status = %d, want 200: %s", available.Code, available.Body.String())
	}
}

func TestResolveOperationDeadlineCancelsInPlacePublicationLoop(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	destination := filepath.Join(storage, "steps", "consumer", "input")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(lagertest.NewTestLogger("publication-deadline"), storage, "node")
	if err := server.ConfigureResolveLimits(1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	server.copyHooks.destinationEntryPublishing = func() { time.Sleep(30 * time.Millisecond) }
	body, _ := json.Marshal(resolveRequest{Key: "producer/output", Dest: destination})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("publication-loop timeout status = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}
}

type contextBlockingTransport struct{}

func (contextBlockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestResolveReportsPeerProbeDeadlineAsTimeout(t *testing.T) {
	storage := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("probe-deadline"), storage, "node")
	if err := server.ConfigureResolveLimits(1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-peer",
			Namespace: "ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}}},
	})
	server.SetPeerResolver(&PeerResolver{
		logger:      lagertest.NewTestLogger("probe"),
		clientset:   clientset,
		namespace:   "ns",
		service:     "artifact-daemon",
		port:        7780,
		myPodIP:     "10.0.0.1",
		probeClient: &http.Client{Transport: contextBlockingTransport{}},
	})
	body, _ := json.Marshal(resolveRequest{
		Key:  "missing/output",
		Dest: filepath.Join(storage, "steps", "consumer", "input"),
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/resolve", bytes.NewReader(body)))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("expired peer probe status = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}
}
