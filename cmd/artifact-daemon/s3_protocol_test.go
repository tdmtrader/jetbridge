package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

const s3ProtocolBucket = "artifacts"

type s3ProtocolObject struct {
	body    []byte
	updated time.Time
}

// s3ProtocolRequest is an immutable record of one request that crossed the
// real AWS SDK boundary. Its fields are semantic S3 inputs, not calls to the
// Store interface that the daemon happens to make internally.
type s3ProtocolRequest struct {
	method string
	key    string
}

type s3Transfer struct {
	method string
	key    string
}

// s3TransferGate holds an actual object transfer at the HTTP boundary. This
// lets concurrency specs establish an overlapping transfer without sleeping.
type s3TransferGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (gate *s3TransferGate) waitUntilEntered(t *testing.T) {
	t.Helper()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("S3 transfer did not reach the protocol server")
	}
}

func (gate *s3TransferGate) open() {
	gate.once.Do(func() { close(gate.release) })
}

// s3ProtocolState is a minimal stateful S3 endpoint. Tests exercise the real
// durable.NewS3 client, including path-style addressing, SigV4, request body
// handling, and ListObjectsV2 XML, while controlling only backend state that a
// real S3-compatible service owns.
type s3ProtocolState struct {
	server *httptest.Server

	mu               sync.Mutex
	objects          map[string]s3ProtocolObject
	requests         []s3ProtocolRequest
	transferGates    map[s3Transfer]*s3TransferGate
	deleteFailures   map[string]int
	omitLastModified bool
}

func newS3ProtocolState(t *testing.T) *s3ProtocolState {
	t.Helper()

	state := &s3ProtocolState{
		objects:        map[string]s3ProtocolObject{},
		transferGates:  map[s3Transfer]*s3TransferGate{},
		deleteFailures: map[string]int{},
	}
	state.server = httptest.NewServer(http.HandlerFunc(state.serveHTTP))
	t.Cleanup(state.server.Close)

	return state
}

func (state *s3ProtocolState) store(t *testing.T) durable.Store {
	t.Helper()

	store, err := durable.NewS3(context.Background(), durable.S3Config{
		Bucket:          s3ProtocolBucket,
		Endpoint:        state.server.URL,
		Region:          "us-east-1",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	return store
}

func (state *s3ProtocolState) omitObjectTimes() {
	state.mu.Lock()
	state.omitLastModified = true
	state.mu.Unlock()
}

func (state *s3ProtocolState) backdateObject(t *testing.T, key string, age time.Duration) {
	t.Helper()

	state.mu.Lock()
	defer state.mu.Unlock()

	object, found := state.objects[key]
	if !found {
		t.Fatalf("cannot backdate absent S3 object %q", key)
	}
	object.updated = time.Now().UTC().Add(-age)
	state.objects[key] = object
}

func (state *s3ProtocolState) makeDeleteUnavailable(key string, status int) {
	state.mu.Lock()
	state.deleteFailures[key] = status
	state.mu.Unlock()
}

func (state *s3ProtocolState) gateTransfer(t *testing.T, method, key string) *s3TransferGate {
	t.Helper()

	gate := &s3TransferGate{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	state.mu.Lock()
	state.transferGates[s3Transfer{method: method, key: key}] = gate
	state.mu.Unlock()

	t.Cleanup(gate.open)

	return gate
}

func (state *s3ProtocolState) requestsFor(method, key string) []s3ProtocolRequest {
	state.mu.Lock()
	defer state.mu.Unlock()

	var requests []s3ProtocolRequest
	for _, request := range state.requests {
		if request.method == method && request.key == key {
			requests = append(requests, request)
		}
	}

	return requests
}

func (state *s3ProtocolState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "request was not signed", http.StatusForbidden)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] != s3ProtocolBucket {
		http.Error(w, "unknown bucket", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		if !state.recordAndGate(r, "") {
			return
		}
		state.listObjects(w, r.URL.Query().Get("prefix"))
		return
	}

	if len(parts) != 2 || parts[1] == "" {
		http.Error(w, "object key is required", http.StatusBadRequest)
		return
	}
	key := parts[1]

	if !state.recordAndGate(r, key) {
		return
	}
	if r.Method == http.MethodDelete {
		state.mu.Lock()
		failureStatus := state.deleteFailures[key]
		state.mu.Unlock()
		if failureStatus != 0 {
			http.Error(w, "object deletion is temporarily unavailable", failureStatus)
			return
		}
	}

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read object body", http.StatusInternalServerError)
			return
		}

		state.mu.Lock()
		state.objects[key] = s3ProtocolObject{
			body:    append([]byte(nil), body...),
			updated: time.Now().UTC(),
		}
		state.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case http.MethodHead:
		object, found, omitLastModified := state.object(key)
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		if !omitLastModified {
			w.Header().Set("Last-Modified", object.updated.Format(http.TimeFormat))
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		object, found, _ := state.object(key)
		if !found {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		w.Header().Set("X-Amz-Checksum-Crc32", crc32Base64(object.body))
		_, _ = w.Write(object.body)

	case http.MethodDelete:
		state.mu.Lock()
		delete(state.objects, key)
		state.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unsupported S3 operation", http.StatusMethodNotAllowed)
	}
}

func crc32Base64(body []byte) string {
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(body))

	return base64.StdEncoding.EncodeToString(checksum[:])
}

// recordAndGate records the protocol request, then holds it if the test put
// that transfer into the unavailable state. False means the request context
// ended before the backend became available.
func (state *s3ProtocolState) recordAndGate(r *http.Request, key string) bool {
	transfer := s3Transfer{method: r.Method, key: key}

	state.mu.Lock()
	state.requests = append(state.requests, s3ProtocolRequest(transfer))
	gate := state.transferGates[transfer]
	state.mu.Unlock()

	if gate == nil {
		return true
	}

	select {
	case gate.entered <- struct{}{}:
	default:
	}

	select {
	case <-gate.release:
		return true
	case <-r.Context().Done():
		return false
	}
}

func (state *s3ProtocolState) object(key string) (s3ProtocolObject, bool, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()

	object, found := state.objects[key]
	object.body = append([]byte(nil), object.body...)

	return object, found, state.omitLastModified
}

type s3ListBucketResult struct {
	XMLName     xml.Name         `xml:"ListBucketResult"`
	XMLNS       string           `xml:"xmlns,attr"`
	Name        string           `xml:"Name"`
	Prefix      string           `xml:"Prefix"`
	KeyCount    int              `xml:"KeyCount"`
	MaxKeys     int              `xml:"MaxKeys"`
	IsTruncated bool             `xml:"IsTruncated"`
	Contents    []s3ListedObject `xml:"Contents"`
}

type s3ListedObject struct {
	Key          string `xml:"Key"`
	Size         int    `xml:"Size"`
	LastModified string `xml:"LastModified,omitempty"`
}

func (state *s3ProtocolState) listObjects(w http.ResponseWriter, prefix string) {
	state.mu.Lock()
	keys := make([]string, 0, len(state.objects))
	for key := range state.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	contents := make([]s3ListedObject, 0, len(keys))
	for _, key := range keys {
		object := state.objects[key]
		listed := s3ListedObject{Key: key, Size: len(object.body)}
		if !state.omitLastModified {
			listed.LastModified = object.updated.Format(time.RFC3339)
		}
		contents = append(contents, listed)
	}
	state.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(s3ListBucketResult{
		XMLNS:       "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:        s3ProtocolBucket,
		Prefix:      prefix,
		KeyCount:    len(contents),
		MaxKeys:     1000,
		IsTruncated: false,
		Contents:    contents,
	})
}
