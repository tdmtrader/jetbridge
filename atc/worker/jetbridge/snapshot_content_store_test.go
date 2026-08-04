package jetbridge

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type closeErrorBody struct {
	io.Reader
	err error
}

func (body *closeErrorBody) Close() error { return body.err }

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
	client.uploadClient = &http.Client{Transport: transport}
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

var testSnapshotArchiveLimits = snapshot.ArchiveLimits{
	MaxContentBytes: 1024,
	MaxEntries:      100,
}

func TestSnapshotContentStoreUsesConfiguredScratchDirectory(t *testing.T) {
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusCreated, nil), nil
	}))
	tempDir := t.TempDir()
	store, err := NewSnapshotContentStore(
		client,
		&locationResolverStub{},
		1,
		testSnapshotArchiveLimits,
		WithSnapshotContentTempDir(tempDir),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.tempDir != tempDir {
		t.Fatalf("snapshot content temp dir = %q, want %q", store.tempDir, tempDir)
	}

	content := testSnapshotArchive(t, "durable")
	if _, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("snapshot scratch leaked files: %v", entries)
	}
}

func testSnapshotArchive(t *testing.T, contents ...string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for index, content := range contents {
		if err := writer.WriteHeader(&tar.Header{
			Name:     fmt.Sprintf("file-%d", index),
			Typeflag: tar.TypeReg,
			Mode:     0644,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestSnapshotContentStorePutRejectsDigestMismatchBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	if err != nil {
		t.Fatal(err)
	}
	actual := testSnapshotArchive(t, "actual")
	wrongDigest := digestFor([]byte("different"))
	locations, err := store.Put(context.Background(), wrongDigest, bytes.NewReader(actual))
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
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, snapshot.ArchiveLimits{MaxContentBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	content := testSnapshotArchive(t, "xx")
	if _, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content)); err == nil || !strings.Contains(err.Error(), "content limit") {
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
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, snapshot.ArchiveLimits{MaxContentBytes: 1, MaxEntries: 1})
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

func TestSnapshotContentStoreRejectsLogicalContentLimitBeforeNetwork(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("xx")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusCreated, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, snapshot.ArchiveLimits{
		MaxContentBytes: 1,
		MaxEntries:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), digestFor(archive.Bytes()), bytes.NewReader(archive.Bytes())); err == nil || !strings.Contains(err.Error(), "content limit") {
		t.Fatalf("Put logical limit error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests.Load())
	}
}

func TestSnapshotContentStorePutUsesDeterministicFactorAndAllowsDegradedSuccess(t *testing.T) {
	content := testSnapshotArchive(t, "replicated archive")
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
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
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

func TestSnapshotContentStorePutUsesOneHangarLocationAndDoesNotMirrorToPeers(t *testing.T) {
	content := testSnapshotArchive(t, "hangar authority")
	digest := digestFor(content)
	var hosts []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-b", "node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Hostname())
		result := response(http.StatusCreated, nil)
		result.Header.Set(snapshotDurableLocationHeader, SnapshotHangarDriver)
		return result, nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	if err != nil {
		t.Fatal(err)
	}

	locations, err := store.Put(context.Background(), digest, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	key, err := hangar.Key(hangar.KindSnapshot, hangar.Digest(digest))
	if err != nil {
		t.Fatal(err)
	}
	want := []snapshot.Location{{Digest: digest, Driver: SnapshotHangarDriver, Key: key}}
	if !equalSnapshotLocations(locations, want) {
		t.Fatalf("locations = %#v, want %#v", locations, want)
	}
	if strings.Join(hosts, ",") != "node-a.test" {
		t.Fatalf("Hangar PUT mirrored to peers: %v", hosts)
	}
}

func TestSnapshotContentStorePutReturnsAggregateFailureWhenNoReplicaAcknowledges(t *testing.T) {
	content := testSnapshotArchive(t, "archive")
	client := snapshotDaemonClient(t, []string{"node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "node-a.test" {
			return nil, fmt.Errorf("node offline")
		}
		return response(http.StatusConflict, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	locations, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content))
	if err == nil || len(locations) != 0 || !strings.Contains(err.Error(), "node-a") || !strings.Contains(err.Error(), "node-b") {
		t.Fatalf("Put = %#v, %v", locations, err)
	}
}

func TestSnapshotContentStorePutReplacesCorruptDigestKeyUsingVerifiedLocalSource(t *testing.T) {
	content := testSnapshotArchive(t, "known good")
	var mutex sync.Mutex
	var methods []string
	putCalls := 0
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		methods = append(methods, request.Method)
		mutex.Unlock()
		switch request.Method {
		case http.MethodPut:
			putCalls++
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(body, content) {
				return nil, fmt.Errorf("PUT body differs from verified local source")
			}
			if putCalls == 1 {
				return response(http.StatusConflict, nil), nil
			}
			return response(http.StatusCreated, nil), nil
		case http.MethodGet:
			return response(http.StatusOK, []byte("corrupt")), nil
		case http.MethodDelete:
			return response(http.StatusNoContent, nil), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	locations, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(locations) != 1 || locations[0].Node != "node-a" {
		t.Fatalf("locations = %#v", locations)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(methods, ",") != "PUT,GET,DELETE,PUT" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestSnapshotContentStorePutReusesVerifiedConflictWithoutDeleting(t *testing.T) {
	content := testSnapshotArchive(t, "already present")
	var mutex sync.Mutex
	var methods []string
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		methods = append(methods, request.Method)
		mutex.Unlock()
		switch request.Method {
		case http.MethodPut:
			return response(http.StatusConflict, nil), nil
		case http.MethodGet:
			return response(http.StatusOK, content), nil
		case http.MethodDelete:
			t.Fatal("verified immutable content must not be deleted")
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	locations, err := store.Put(context.Background(), digestFor(content), bytes.NewReader(content))
	if err != nil || len(locations) != 1 {
		t.Fatalf("Put = %#v, %v", locations, err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(methods, ",") != "PUT,GET" {
		t.Fatalf("methods = %v", methods)
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
	store, _ := NewSnapshotContentStore(client, resolver, 2, testSnapshotArchiveLimits)
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
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
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
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	reader, err := store.Open(context.Background(), snapshotFor(content))
	if err == nil || reader != nil || !strings.Contains(err.Error(), "length") {
		t.Fatalf("Open = %#v, %v", reader, err)
	}
}

func TestSnapshotContentStoreExistsCryptographicallyVerifiesAndDeletesUseStrictStableLocation(t *testing.T) {
	content := []byte("archive")
	digest := digestFor(content)
	valid := snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: snapshotKeyForDigest(digest), Node: "node-a"}
	var methodsMutex sync.Mutex
	var methods []string
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		methodsMutex.Lock()
		methods = append(methods, request.Method)
		methodsMutex.Unlock()
		if request.Method == http.MethodGet {
			return response(http.StatusOK, content), nil
		}
		if request.Header.Get("X-Concourse-Snapshot-Delete-Cache-Only") != "true" {
			t.Fatal("legacy daemon cache deletion must not delete the durable Hangar object")
		}
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
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
	if strings.Join(methods, ",") != "GET,DELETE" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestSnapshotContentStoreExistsRejectsPresentButCorruptReplica(t *testing.T) {
	content := []byte("known-good archive")
	location := snapshot.Location{
		Digest: digestFor(content), Driver: SnapshotDaemonDriver,
		Key: snapshotKeyForDigest(digestFor(content)), Node: "node-a",
	}
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatalf("verification method = %s, want GET", request.Method)
		}
		return response(http.StatusOK, []byte("corrupt")), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	exists, err := store.Exists(context.Background(), location)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("Exists accepted bytes whose digest does not match the recorded location")
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
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	err := store.DeleteAll(context.Background(), digestFor(content))
	if err == nil || !strings.Contains(err.Error(), "node-b") || requests.Load() != 3 {
		t.Fatalf("DeleteAll = %v, requests=%d", err, requests.Load())
	}
}

// DeleteAll is the only reclamation the lifecycle performs for a digest that
// never reached a manifest, so it must release the durable Hangar object too.
// While it broadcast a cache-only delete, every failed upload leaked its bytes
// forever: the collect pass removed the staged row, the DB forgot the digest,
// and the durable object became unreachable by construction.
func TestSnapshotContentStoreDeleteAllRequestsDurableDeletion(t *testing.T) {
	content := []byte("archive")
	var cacheOnly []string
	var mutex sync.Mutex
	client := snapshotDaemonClient(t, []string{"node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		cacheOnly = append(cacheOnly, request.Header.Get("X-Concourse-Snapshot-Delete-Cache-Only"))
		mutex.Unlock()
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	if err := store.DeleteAll(context.Background(), digestFor(content)); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(cacheOnly) != 2 {
		t.Fatalf("delete requests = %d, want 2", len(cacheOnly))
	}
	for _, value := range cacheOnly {
		if value == "true" {
			t.Fatal("DeleteAll requested a cache-only delete, which orphans the durable Hangar object forever")
		}
	}
}

func TestSnapshotContentStoreHonorsCancellation(t *testing.T) {
	content := testSnapshotArchive(t, "archive")
	started := make(chan struct{})
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
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

func TestSnapshotContentStoreRepairRejectsUntrustedLocationsBeforeNetwork(t *testing.T) {
	content := []byte("archive")
	value := snapshotFor(content)
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	location := snapshot.Location{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: "../../secret", Node: "node-a"}
	if _, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location}); err == nil {
		t.Fatal("RepairReplicas accepted an arbitrary storage key")
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests.Load())
	}
}

func TestSnapshotContentStoreRepairTreatsHangarAsOneDurableLocation(t *testing.T) {
	content := []byte("canonical Hangar archive")
	value := snapshotFor(content)
	location := hangarSnapshotLocation(value.Digest)
	var hosts []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-b", "node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Hostname())
		if request.Method != http.MethodGet {
			return nil, fmt.Errorf("Hangar repair must not mirror: %s", request.Method)
		}
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 3, testSnapshotArchiveLimits)

	result, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified != 1 || result.Desired != 1 || result.LiveCapacity != 1 ||
		len(result.Added) != 0 || len(result.Removed) != 0 {
		t.Fatalf("Hangar repair result = %#v", result)
	}
	if strings.Join(hosts, ",") != "node-a.test" {
		t.Fatalf("Hangar repair reached peers instead of one cache endpoint: %v", hosts)
	}
}

func TestSnapshotContentStoreRepairFallsThroughCorruptionAndRewritesAfterVerifiedSource(t *testing.T) {
	content := []byte("canonical archive")
	corrupt := []byte("corrupt archive!!")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	recorded := []snapshot.Location{
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a"},
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-b"},
	}
	var mutex sync.Mutex
	var methods []string
	client := snapshotDaemonClient(t, []string{"node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		methods = append(methods, request.Method+":"+request.URL.Hostname())
		mutex.Unlock()
		switch request.Method {
		case http.MethodGet:
			if request.URL.Hostname() == "node-a.test" {
				return response(http.StatusOK, corrupt), nil
			}
			return response(http.StatusOK, content), nil
		case http.MethodDelete:
			return response(http.StatusNoContent, nil), nil
		case http.MethodPut:
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(body, content) {
				return nil, fmt.Errorf("repair body = %q", body)
			}
			return response(http.StatusCreated, nil), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	result, err := store.RepairReplicas(context.Background(), value, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified != 2 || result.Desired != 2 || len(result.Added) != 1 || result.Added[0].Node != "node-a" {
		t.Fatalf("repair result = %#v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	joined := strings.Join(methods, ",")
	if !strings.Contains(joined, "GET:node-a.test") || !strings.Contains(joined, "GET:node-b.test") ||
		!strings.Contains(joined, "DELETE:node-a.test") || !strings.Contains(joined, "PUT:node-a.test") {
		t.Fatalf("repair methods = %v", methods)
	}
}

func TestSnapshotContentStoreRepairRemovesSpoolWhenSourceCloseFails(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	content := []byte("canonical archive")
	value := snapshotFor(content)
	closeFailure := errors.New("source close failed")
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		result := response(http.StatusOK, content)
		result.Body = &closeErrorBody{Reader: bytes.NewReader(content), err: closeFailure}
		return result, nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	path, err := store.verifyRepairEndpoint(context.Background(), DaemonEndpoint{NodeName: "node-a", Address: "node-a.test"}, value, true)
	if !errors.Is(err, closeFailure) || path != "" {
		t.Fatalf("verifyRepairEndpoint = %q, %v; want source close failure", path, err)
	}
	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("spool directory contains leaked files: %v", entries)
	}
}

func TestSnapshotContentStoreRepairWithoutReadableSourceIsNonDestructive(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	location := snapshot.Location{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a"}
	var deletes atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodDelete || request.Method == http.MethodPut {
			deletes.Add(1)
		}
		return response(http.StatusNotFound, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	result, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if !errors.Is(err, snapshot.ErrNoReadableReplica) {
		t.Fatalf("RepairReplicas error = %v", err)
	}
	if len(result.Added) != 0 || len(result.Removed) != 0 || deletes.Load() != 0 {
		t.Fatalf("destructive source-less repair: result=%#v writes=%d", result, deletes.Load())
	}
}

func TestSnapshotContentStoreRepairRecoversUnrecordedAcknowledgedReplica(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)
	result, err := store.RepairReplicas(context.Background(), value, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Desired != 1 || result.LiveCapacity != 1 || result.Verified != 1 ||
		len(result.Added) != 1 || result.Added[0].Node != "node-a" {
		t.Fatalf("repair result = %#v", result)
	}
}

func TestSnapshotContentStoreRepairReturnsSortedPartialAdditionsWhenLaterTargetFails(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	recorded := []snapshot.Location{{
		Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a",
	}}
	var mutex sync.Mutex
	var requests []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-a", "node-b"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestName := request.Method + ":" + request.URL.Hostname()
		mutex.Lock()
		requests = append(requests, requestName)
		mutex.Unlock()
		switch requestName {
		case "GET:node-a.test":
			return response(http.StatusOK, content), nil
		case "PUT:node-b.test":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(body, content) {
				return nil, fmt.Errorf("node-b repair body = %q", body)
			}
			return response(http.StatusCreated, nil), nil
		case "PUT:node-c.test":
			return response(http.StatusServiceUnavailable, nil), nil
		default:
			return nil, fmt.Errorf("unexpected repair request %s", requestName)
		}
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 3, testSnapshotArchiveLimits)

	result, err := store.RepairReplicas(context.Background(), value, recorded)
	if err == nil {
		t.Fatal("RepairReplicas succeeded despite the final target failure")
	}
	wantAdded := []snapshot.Location{{
		Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-b",
	}}
	if !equalSnapshotLocations(result.Added, wantAdded) || len(result.Removed) != 0 ||
		result.Verified != 2 || result.Desired != 3 || result.LiveCapacity != 3 {
		t.Fatalf("partial repair result = %#v, want node-b preserved", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(requests, ",") != "GET:node-a.test,PUT:node-b.test,PUT:node-c.test" {
		t.Fatalf("repair requests = %v", requests)
	}
}

func TestSnapshotContentStoreRepairPreservesRecordedOfflineLocation(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	recorded := []snapshot.Location{
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a"},
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-0-offline"},
	}
	var requests atomic.Int64
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Hostname() != "node-a.test" {
			return nil, fmt.Errorf("unexpected repair request %s:%s", request.Method, request.URL.Hostname())
		}
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)

	result, err := store.RepairReplicas(context.Background(), value, recorded)
	if err != nil {
		t.Fatalf("RepairReplicas: %v", err)
	}
	if result.Verified != 1 || result.Desired != 1 || len(result.Added) != 0 || len(result.Removed) != 0 {
		t.Fatalf("offline recorded location was mutated: %#v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want only the live recorded source", requests.Load())
	}
}

func TestSnapshotContentStoreRepairFactorSatisfiedDoesNotProbeUnrecordedNodes(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	recorded := []snapshot.Location{{
		Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a",
	}}
	var mutex sync.Mutex
	var requests []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-b", "node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestName := request.Method + ":" + request.URL.Hostname()
		mutex.Lock()
		requests = append(requests, requestName)
		mutex.Unlock()
		if requestName != "GET:node-a.test" {
			return nil, fmt.Errorf("unrelated node must not affect factor-satisfied repair: %s", requestName)
		}
		return response(http.StatusOK, content), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	result, err := store.RepairReplicas(context.Background(), value, recorded)
	if err != nil {
		t.Fatalf("RepairReplicas: %v", err)
	}
	if result.Verified != 1 || result.Desired != 1 || len(result.Added) != 0 || len(result.Removed) != 0 {
		t.Fatalf("factor-satisfied result = %#v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(requests, ",") != "GET:node-a.test" {
		t.Fatalf("repair requests = %v", requests)
	}
}

func TestSnapshotContentStoreRepairProbesOnlyUntilUnrecordedSourceThenCopiesDeterministically(t *testing.T) {
	content := []byte("canonical archive")
	value := snapshotFor(content)
	key := snapshotKeyForDigest(value.Digest)
	var mutex sync.Mutex
	var requests []string
	client := snapshotDaemonClient(t, []string{"node-c", "node-b", "node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestName := request.Method + ":" + request.URL.Hostname()
		mutex.Lock()
		requests = append(requests, requestName)
		mutex.Unlock()
		switch requestName {
		case "GET:node-a.test":
			return response(http.StatusNotFound, nil), nil
		case "GET:node-b.test":
			return response(http.StatusOK, content), nil
		case "PUT:node-a.test":
			return response(http.StatusCreated, nil), nil
		default:
			return nil, fmt.Errorf("unexpected repair request %s", requestName)
		}
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 2, testSnapshotArchiveLimits)

	result, err := store.RepairReplicas(context.Background(), value, nil)
	if err != nil {
		t.Fatalf("RepairReplicas: %v", err)
	}
	wantAdded := []snapshot.Location{
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-a"},
		{Digest: value.Digest, Driver: SnapshotDaemonDriver, Key: key, Node: "node-b"},
	}
	if !equalSnapshotLocations(result.Added, wantAdded) || result.Verified != 2 || result.Desired != 2 {
		t.Fatalf("repair result = %#v, want sorted recovered and copied locations", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(requests, ",") != "GET:node-a.test,GET:node-b.test,PUT:node-a.test" {
		t.Fatalf("repair requests = %v", requests)
	}
}

func equalSnapshotLocations(left, right []snapshot.Location) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
