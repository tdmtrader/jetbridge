package jetbridge

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/snapshot"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type locationResolverStub struct {
	locations []snapshot.Location
	err       error
	calls     atomic.Int64
}

func (resolver *locationResolverStub) LocationsForDigest(context.Context, snapshot.Digest) ([]snapshot.Location, error) {
	resolver.calls.Add(1)
	return append([]snapshot.Location(nil), resolver.locations...), resolver.err
}

func digestFor(data []byte) snapshot.Digest {
	sum := sha256.Sum256(data)
	return snapshot.Digest(fmt.Sprintf("sha256:%x", sum[:]))
}

func snapshotFor(data []byte) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:             1,
		Type:           "opaque/v1",
		Digest:         digestFor(data),
		ByteSize:       int64(len(data)),
		FileCount:      1,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Now().UTC(),
	}
}

func endpointSliceForNodes(namespace, service string, nodes ...string) *discoveryv1.EndpointSlice {
	endpoints := make([]discoveryv1.Endpoint, 0, len(nodes))
	for _, node := range nodes {
		nodeCopy := node
		endpoints = append(endpoints, discoveryv1.Endpoint{
			NodeName:  &nodeCopy,
			Addresses: []string{node + ".test"},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service + "-slice",
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		Endpoints: endpoints,
	}
}

func snapshotDaemonClient(t *testing.T, nodes []string, transport http.RoundTripper) *DaemonClient {
	t.Helper()
	client := NewDaemonClient(
		lagertest.NewTestLogger("snapshot-store"),
		fake.NewSimpleClientset(endpointSliceForNodes("cicd", "artifact-daemon", nodes...)),
		"cicd",
		"artifact-daemon",
		7780,
		nil,
	)
	client.streamingClient = &http.Client{Transport: transport}
	client.client = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	return client
}

func TestDaemonSnapshotTransportHasNoWholeTransferTimeout(t *testing.T) {
	client := NewDaemonClient(
		lagertest.NewTestLogger("streaming"),
		fake.NewSimpleClientset(),
		"cicd",
		"artifact-daemon",
		7780,
		nil,
	)
	if client.streamingClient.Timeout != 0 {
		t.Fatalf("streaming client timeout = %s, want no whole-transfer timeout", client.streamingClient.Timeout)
	}
	transport, ok := client.streamingClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("streaming transport must retain a bounded response-header timeout: %#v", client.streamingClient.Transport)
	}
}

func response(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": []string{"application/x-tar"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestSnapshotContentStorePutRejectsDigestMismatchBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	wrongDigest := digestFor([]byte("different"))
	locations, err := store.Put(context.Background(), wrongDigest, strings.NewReader("actual"))
	if err == nil || len(locations) != 0 {
		t.Fatalf("Put = %#v, %v; want digest error", locations, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests.Load())
	}
}

func TestSnapshotContentStorePutEnforcesMaximumBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("four")
	if _, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content)); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("Put maximum error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests.Load())
	}
}

func TestSnapshotContentStoreAcceptsCanonicalTarOverheadForSmallContentLimit(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archiveLimit, err := snapshot.CanonicalArchiveByteLimit(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, archiveLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), digestFor(archive.Bytes()), bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestSnapshotContentStorePutUsesDeterministicFactorAndAllowsDegradedSuccess(t *testing.T) {
	content := []byte("replicated archive")
	var mutex sync.Mutex
	var hosts []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-b", "node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(body, content) {
			return nil, fmt.Errorf("unexpected upload bytes %q", body)
		}
		mutex.Lock()
		hosts = append(hosts, request.URL.Hostname())
		mutex.Unlock()
		if request.URL.Hostname() == "node-b.test" {
			return response(http.StatusInternalServerError, nil), nil
		}
		// 200 is the daemon's idempotent acknowledgement; 201 is covered by
		// the immutable daemon endpoint suite.
		return response(http.StatusOK, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].Node != "node-a" || locations[0].Driver != SnapshotDaemonDriver {
		t.Fatalf("locations = %#v", locations)
	}
	mutex.Lock()
	sort.Strings(hosts)
	gotHosts := append([]string(nil), hosts...)
	mutex.Unlock()
	if strings.Join(gotHosts, ",") != "node-a.test,node-b.test" {
		t.Fatalf("upload hosts = %v", gotHosts)
	}
}

func TestSnapshotContentStorePutReturnsAggregateFailureWhenNoReplicaAcknowledges(t *testing.T) {
	content := []byte("archive")
	client := snapshotDaemonClient(t, []string{"node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "node-a.test" {
			return nil, fmt.Errorf("node offline")
		}
		return response(http.StatusConflict, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, 1024)
	locations, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content))
	if err == nil || len(locations) != 0 || !strings.Contains(err.Error(), "node-a") || !strings.Contains(err.Error(), "node-b") {
		t.Fatalf("Put = %#v, %v", locations, err)
	}
}

func TestSnapshotContentStoreOpenTriesRecordedReplicaBeforeLiveFallback(t *testing.T) {
	content := []byte("read fallback archive")
	digest := digestFor(content)
	key := snapshotKeyForDigest(digest)
	resolver := &locationResolverStub{locations: []snapshot.Location{{
		Digest: digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-b",
	}}}
	var mutex sync.Mutex
	var hosts []string
	client := snapshotDaemonClient(t, []string{"node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		hosts = append(hosts, request.URL.Hostname())
		mutex.Unlock()
		if request.URL.Hostname() == "node-b.test" {
			return response(http.StatusNotFound, nil), nil
		}
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, resolver, 2, 1024)
	reader, err := store.Open(context.Background(), snapshotFor(content))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || !bytes.Equal(got, content) {
		t.Fatalf("read = %q, %v, close %v", got, err, closeErr)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(hosts, ",") != "node-b.test,node-a.test" {
		t.Fatalf("read hosts = %v", hosts)
	}
}

func TestSnapshotContentStoreOpenReportsCorruptionAtEOF(t *testing.T) {
	want := []byte("expected")
	corrupt := []byte("corrupt!")
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, corrupt), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, 1024)
	reader, err := store.Open(context.Background(), snapshotFor(want))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err == nil || !strings.Contains(err.Error(), "digest") || !bytes.Equal(got, corrupt) {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestSnapshotContentStoreOpenRejectsDeclaredLengthBeforeExposingBytes(t *testing.T) {
	content := []byte("archive")
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		result := response(http.StatusOK, content)
		result.ContentLength++
		return result, nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, 1024)
	reader, err := store.Open(context.Background(), snapshotFor(content))
	if err == nil || reader != nil || !strings.Contains(err.Error(), "length") {
		t.Fatalf("Open = %#v, %v", reader, err)
	}
}

func TestSnapshotContentStoreExistsAndDeletesUseStrictStableLocation(t *testing.T) {
	content := []byte("archive")
	digest := digestFor(content)
	valid := snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: snapshotKeyForDigest(digest), Node: "node-a"}
	var methodsMutex sync.Mutex
	var methods []string
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		methodsMutex.Lock()
		methods = append(methods, request.Method)
		methodsMutex.Unlock()
		if request.Method == http.MethodHead {
			return response(http.StatusOK, nil), nil
		}
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, 1024)
	exists, err := store.Exists(context.Background(), valid)
	if err != nil || !exists {
		t.Fatalf("Exists = %v, %v", exists, err)
	}
	if err := store.DeleteLocation(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Node = ""
	if _, err := store.Exists(context.Background(), invalid); err == nil {
		t.Fatal("expected location without stable node identity to be rejected")
	}
	if err := store.DeleteLocation(context.Background(), snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: valid.Key, Node: "absent"}); err == nil {
		t.Fatal("expected absent node to remain retryable")
	}
	methodsMutex.Lock()
	defer methodsMutex.Unlock()
	if strings.Join(methods, ",") != "HEAD,DELETE" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestSnapshotContentStoreDeleteAllBroadcastsAndAggregates(t *testing.T) {
	content := []byte("archive")
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a", "node-b", "node-c"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Method != http.MethodDelete {
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
		if request.URL.Hostname() == "node-b.test" {
			return nil, fmt.Errorf("unreachable")
		}
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, 1024)
	err := store.DeleteAll(context.Background(), digestFor(content))
	if err == nil || !strings.Contains(err.Error(), "node-b") || requests.Load() != 3 {
		t.Fatalf("DeleteAll = %v, requests=%d", err, requests.Load())
	}
}

func TestSnapshotContentStoreHonorsCancellation(t *testing.T) {
	content := []byte("archive")
	started := make(chan struct{})
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.Put(ctx, digestFor(content), bytes.NewReader(content))
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("Put cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Put did not honor cancellation")
	}
}
