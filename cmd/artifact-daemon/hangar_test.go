package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/hangar"
)

type hangarStoreStub struct {
	ensure func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error)
	open   func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error)
}

func (store *hangarStoreStub) EnsureTree(ctx context.Context, scope hangar.Scope, digest hangar.Digest, body io.Reader, limit int64) (hangar.TreeAttributes, bool, error) {
	return store.ensure(ctx, scope, digest, body, limit)
}
func (store *hangarStoreStub) InspectTree(context.Context, hangar.Scope, hangar.Digest, int64) (hangar.TreeAttributes, error) {
	panic("unexpected InspectTree")
}
func (store *hangarStoreStub) OpenTree(ctx context.Context, ref hangar.TreeRef, limit int64) (io.ReadCloser, hangar.TreeAttributes, error) {
	return store.open(ctx, ref, limit)
}
func (store *hangarStoreStub) DeleteTree(context.Context, hangar.TreeRef) error {
	panic("unexpected DeleteTree")
}

func rawHangarTar(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(content)), ModTime: time.Unix(99, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func canonicalHangarTree(t *testing.T, scratch string, raw []byte) ([]byte, hangar.Digest) {
	t.Helper()
	tree, err := (hangar.Canonicalizer{TempDir: scratch, MaxEntries: 10, MaxContentBytes: 1024}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	content, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return content, tree.Digest
}

func newHangarTestServer(t *testing.T, store hangar.Store) (*Server, *HangarService, []byte) {
	t.Helper()
	return newHangarTestServerWithLogger(t, store, lagertest.NewTestLogger("hangar-daemon"))
}

func newHangarTestServerWithLogger(t *testing.T, store hangar.Store, logger lager.Logger) (*Server, *HangarService, []byte) {
	t.Helper()
	storage := t.TempDir()
	scratch := t.TempDir()
	key := bytes.Repeat([]byte{0x42}, 32)
	verifier, err := hangar.NewGrantVerifier(key, hangar.MaxGrantTTL, nil)
	if err != nil {
		t.Fatal(err)
	}
	canonicalizer := hangar.Canonicalizer{TempDir: scratch, MaxEntries: 10, MaxContentBytes: 1024}
	service := &HangarService{
		Store: store, Canonicalizer: canonicalizer, GrantVerifier: verifier,
		Materializer:    &hangar.Materializer{Store: store, Canonicalizer: canonicalizer, StoragePath: storage, MaxTreeBytes: 1 << 20},
		MaxContentBytes: 1024, MaxEntries: 10, MaxArchiveBytes: 1 << 20, MaxControlBytes: 16 << 10,
	}
	server, err := NewServer(logger, storage, "node-a")
	if err != nil {
		t.Fatalf("NewServer(%q): %v", storage, err)
	}
	server.SetHangarService(service)
	t.Cleanup(func() {
		_ = filepath.Walk(storage, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0700)
			} else if err == nil {
				_ = os.Chmod(path, 0600)
			}
			return nil
		})
	})
	return server, service, key
}

func TestHangarDisabledRoutesAre404(t *testing.T) {
	storage := t.TempDir()
	server, err := NewServer(lagertest.NewTestLogger("disabled"), storage, "node")
	if err != nil {
		t.Fatalf("NewServer(%q): %v", storage, err)
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/hangar/v1/scopes/ci/trees"},
		{http.MethodGet, "/hangar/v1/scopes/ci/trees/sha256/" + strings.Repeat("a", 64) + "/generations/1"},
		{http.MethodPost, "/hangar/v1/materializations"},
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", request.method, request.path, recorder.Code)
		}
	}
}

func TestHangarPublishCanonicalizesAndReturnsExactAttributes(t *testing.T) {
	raw := rawHangarTar(t, "out.txt", "payload")
	scratch := t.TempDir()
	canonical, digest := canonicalHangarTree(t, scratch, raw)
	createdAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	var calls int
	store := &hangarStoreStub{}
	store.ensure = func(_ context.Context, scope hangar.Scope, gotDigest hangar.Digest, source io.Reader, _ int64) (hangar.TreeAttributes, bool, error) {
		calls++
		got, err := io.ReadAll(source)
		if err != nil {
			t.Fatal(err)
		}
		if scope != "ci" || gotDigest != digest || !bytes.Equal(got, canonical) {
			t.Fatalf("EnsureTree received scope=%q digest=%q canonical=%v", scope, gotDigest, bytes.Equal(got, canonical))
		}
		return hangar.TreeAttributes{Ref: hangar.TreeRef{Scope: scope, Digest: digest, Generation: 7}, StoredBytes: 91, LogicalBytes: int64(len(got)), CreatedAt: createdAt}, calls == 1, nil
	}
	server, _, _ := newHangarTestServer(t, store)
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/x-tar")
		server.Handler().ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("publish = %d body=%q, want %d", recorder.Code, recorder.Body.String(), want)
		}
		var got hangar.TreeAttributes
		var vocabulary map[string]json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &vocabulary); err != nil {
			t.Fatal(err)
		}
		if len(vocabulary) != 4 {
			t.Fatalf("publication response vocabulary: %v", vocabulary)
		}
		var refVocabulary map[string]json.RawMessage
		if err := json.Unmarshal(vocabulary["ref"], &refVocabulary); err != nil {
			t.Fatal(err)
		}
		if len(refVocabulary) != 3 {
			t.Fatalf("publication ref vocabulary: %v", refVocabulary)
		}
		for _, key := range []string{"scope", "digest", "generation"} {
			delete(refVocabulary, key)
		}
		if len(refVocabulary) != 0 {
			t.Fatalf("publication ref has nonexact keys: %v", refVocabulary)
		}
		for _, key := range []string{"ref", "stored_bytes", "logical_bytes", "created_at"} {
			delete(vocabulary, key)
		}
		if len(vocabulary) != 0 {
			t.Fatalf("publication response has nonexact keys: %v", vocabulary)
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Ref.Generation != 7 || got.Ref.Digest != digest || got.Ref.Scope != "ci" || got.StoredBytes != 91 || got.LogicalBytes != int64(len(canonical)) || !got.CreatedAt.Equal(createdAt) {
			t.Fatalf("attributes = %#v", got)
		}
	}
}

type failingReadCloser struct{ closeErr error }

func (*failingReadCloser) Read([]byte) (int, error) {
	return 0, fmt.Errorf("corrupt after open: %w", hangar.ErrCorrupt)
}
func (reader *failingReadCloser) Close() error { return reader.closeErr }

func TestHangarOpenFullyVerifiesBeforeWritingSuccess(t *testing.T) {
	digest := hangar.Digest("sha256:" + strings.Repeat("a", 64))
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 9}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		return &failingReadCloser{}, hangar.TreeAttributes{Ref: ref}, nil
	}
	server, _, _ := newHangarTestServer(t, store)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hangar/v1/scopes/ci/trees/sha256/"+strings.Repeat("a", 64)+"/generations/9", nil))
	if recorder.Code != http.StatusUnprocessableEntity || strings.Contains(recorder.Body.String(), "corrupt after open") {
		t.Fatalf("open = %d %q, want sanitized 422", recorder.Code, recorder.Body.String())
	}
}

func TestHangarOpenHashesActualSpooledBytesBeforeSuccess(t *testing.T) {
	raw := rawHangarTar(t, "x", "payload")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	mutated := append([]byte(nil), canonical...)
	mutated[len(mutated)/2] ^= 0x01
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 4}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		return io.NopCloser(bytes.NewReader(mutated)), hangar.TreeAttributes{Ref: ref, LogicalBytes: int64(len(mutated))}, nil
	}
	server, _, _ := newHangarTestServer(t, store)
	recorder := httptest.NewRecorder()
	path := "/hangar/v1/scopes/ci/trees/sha256/" + strings.TrimPrefix(string(digest), "sha256:") + "/generations/4"
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusUnprocessableEntity || recorder.Body.String() != "tree verification failed\n" {
		t.Fatalf("same-length mutation = %d %q, want sanitized 422", recorder.Code, recorder.Body.String())
	}
}

type hangarErrorReader struct{ err error }

func (reader hangarErrorReader) Read([]byte) (int, error) { return 0, reader.err }
func (hangarErrorReader) Close() error                    { return nil }

type hangarReadCloseErrorReader struct {
	readErr  error
	closeErr error
}

func (reader hangarReadCloseErrorReader) Read([]byte) (int, error) { return 0, reader.readErr }
func (reader hangarReadCloseErrorReader) Close() error             { return reader.closeErr }

func TestHangarOpenClassifiesReadAndCloseFailuresTogether(t *testing.T) {
	digest := hangar.Digest("sha256:" + strings.Repeat("d", 64))
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 6}
	for _, tc := range []struct {
		name     string
		readErr  error
		closeErr error
		status   int
		body     string
	}{
		{"not-found-read-infrastructure-close", hangar.ErrNotFound, hangar.ErrInfrastructure, 503, "service unavailable\n"},
		{"corrupt-read-context-close", hangar.ErrCorrupt, context.Canceled, 503, "service unavailable\n"},
		{"untyped-read-conflict-close", errors.New("local spool read"), hangar.ErrConflict, 503, "service unavailable\n"},
		{"not-found-read-untyped-close", hangar.ErrNotFound, errors.New("local close"), 503, "service unavailable\n"},
		{"joined-not-found-and-untyped-read", errors.Join(hangar.ErrNotFound, errors.New("backend read failed")), nil, 503, "service unavailable\n"},
		{"joined-conflict-and-untyped-close", io.EOF, errors.Join(hangar.ErrConflict, errors.New("backend close failed")), 503, "service unavailable\n"},
		{"high-precedence-typed-alone", hangar.ErrCorrupt, nil, 422, "tree verification failed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
				panic("unexpected")
			}}
			store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
				return hangarReadCloseErrorReader{readErr: tc.readErr, closeErr: tc.closeErr}, hangar.TreeAttributes{Ref: ref}, nil
			}
			server, _, _ := newHangarTestServer(t, store)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hangar/v1/scopes/ci/trees/sha256/"+strings.Repeat("d", 64)+"/generations/6", nil))
			if recorder.Code != tc.status || recorder.Body.String() != tc.body {
				t.Fatalf("status=%d body=%q, want sanitized %d %q", recorder.Code, recorder.Body.String(), tc.status, tc.body)
			}
		})
	}
}

func TestHangarOpenPreservesTypedReadAndCloseFailures(t *testing.T) {
	digest := hangar.Digest("sha256:" + strings.Repeat("c", 64))
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 5}
	for _, tc := range []struct {
		name   string
		reader io.ReadCloser
		want   int
	}{
		{"infrastructure-read", hangarErrorReader{err: errors.Join(hangar.ErrInfrastructure, errors.New("backend"))}, 503},
		{"context-read", hangarErrorReader{err: context.Canceled}, 503},
		{"close", &closeErrorReader{Reader: bytes.NewReader(nil), err: hangar.ErrInfrastructure}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
				panic("unexpected")
			}}
			store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
				return tc.reader, hangar.TreeAttributes{Ref: ref}, nil
			}
			server, _, _ := newHangarTestServer(t, store)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hangar/v1/scopes/ci/trees/sha256/"+strings.Repeat("c", 64)+"/generations/5", nil))
			if recorder.Code != tc.want || recorder.Body.String() != "service unavailable\n" {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type closeErrorReader struct {
	*bytes.Reader
	err error
}

func (reader *closeErrorReader) Close() error { return reader.err }

func TestHangarOpenExactGeneration(t *testing.T) {
	raw := rawHangarTar(t, "x", "y")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 3}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(_ context.Context, got hangar.TreeRef, _ int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		if got != ref {
			t.Fatalf("ref = %#v, want %#v", got, ref)
		}
		return io.NopCloser(bytes.NewReader(canonical)), hangar.TreeAttributes{Ref: ref, LogicalBytes: int64(len(canonical))}, nil
	}
	server, _, _ := newHangarTestServer(t, store)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hangar/v1/scopes/ci/trees/sha256/"+strings.TrimPrefix(string(digest), "sha256:")+"/generations/3", nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/x-tar" || recorder.Header().Get("Content-Length") == "" || !bytes.Equal(recorder.Body.Bytes(), canonical) {
		t.Fatalf("open = %d headers=%v exact=%v body=%q", recorder.Code, recorder.Header(), bytes.Equal(recorder.Body.Bytes(), canonical), recorder.Body.String())
	}
}

func TestHangarTypedStatuses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"not-found", hangar.ErrNotFound, 404}, {"conflict", hangar.ErrConflict, 409}, {"limit", hangar.ErrLimitExceeded, 413},
		{"corrupt", hangar.ErrCorrupt, 422}, {"infrastructure", hangar.ErrInfrastructure, 503}, {"unauthorized", hangar.ErrUnauthorized, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
				return hangar.TreeAttributes{}, false, tc.err
			}}
			server, _, _ := newHangarTestServer(t, store)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", bytes.NewReader(rawHangarTar(t, "x", "y"))))
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%q want=%d", recorder.Code, recorder.Body.String(), tc.status)
			}
		})
	}
}

func TestHangarStatusPrecedenceNeverDowngradesCompoundFailuresToNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"infrastructure", errors.Join(hangar.ErrNotFound, hangar.ErrInfrastructure), 503},
		{"context", errors.Join(hangar.ErrNotFound, context.Canceled), 503},
		{"unauthorized", errors.Join(hangar.ErrNotFound, hangar.ErrUnauthorized), 401},
		{"corrupt", errors.Join(hangar.ErrNotFound, hangar.ErrCorrupt), 422},
		{"limit", errors.Join(hangar.ErrNotFound, hangar.ErrLimitExceeded), 413},
		{"conflict", errors.Join(hangar.ErrNotFound, hangar.ErrConflict), 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := newHangarTestServer(t, &hangarStoreStub{})
			recorder := httptest.NewRecorder()
			server.refuseHangar(recorder, httptest.NewRequest(http.MethodGet, "/hangar/v1/scopes/ci/trees", nil), tc.err)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d body=%q want=%d", recorder.Code, recorder.Body.String(), tc.want)
			}
		})
	}
}

func TestHangarRejectsMalformedAndOversizedRequests(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("must not store")
	}}
	server, service, _ := newHangarTestServer(t, store)
	service.MaxArchiveBytes = 16
	for _, tc := range []struct {
		name, path, body string
		status           int
	}{
		{"scope", "/hangar/v1/scopes/INVALID/trees", string(rawHangarTar(t, "x", "y")), 400},
		{"raw-limit", "/hangar/v1/scopes/ci/trees", strings.Repeat("x", 17), 413},
		{"json-unknown", "/hangar/v1/materializations", `{"items":[],"unknown":true}`, 400},
		{"json-trailing", "/hangar/v1/materializations", `{"items":[]} {}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%q want=%d", recorder.Code, recorder.Body.String(), tc.status)
			}
		})
	}
}

func TestHangarProtectedTreeRoutesRequireMTLSButMaterializationDoesNot(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, _, _ := newHangarTestServer(t, store)
	for _, path := range []string{"/hangar/v1/scopes/ci/trees", "/hangar/v1/scopes/ci/trees/sha256/" + strings.Repeat("a", 64) + "/generations/1"} {
		method := http.MethodPost
		if strings.Contains(path, "/sha256/") {
			method = http.MethodGet
		}
		recorder := httptest.NewRecorder()
		server.Handler(WithTLS()).ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	server.Handler(WithTLS()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(`{"items":[]}`)))
	if recorder.Code == http.StatusUnauthorized {
		t.Fatal("materialization was incorrectly protected by mTLS")
	}

	recorder = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/INVALID/trees", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	server.Handler(WithTLS()).ServeHTTP(recorder, req)
	if recorder.Code == http.StatusUnauthorized {
		t.Fatal("verified-client request was rejected")
	}
}

func TestHangarMaterializationAuthorizesEntireBatchBeforeMutation(t *testing.T) {
	raw := rawHangarTar(t, "file", "content")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 1}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	var opens int
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		opens++
		return io.NopCloser(bytes.NewReader(canonical)), hangar.TreeAttributes{Ref: ref, LogicalBytes: int64(len(canonical))}, nil
	}
	server, _, key := newHangarTestServer(t, store)
	signer, _ := hangar.NewGrantSigner(key, time.Minute, nil)
	valid, _ := signer.Sign(ref, "handle", "volume-a")
	body := map[string]any{"items": []any{
		map[string]any{"ref": ref, "handle": "handle", "volume": "volume-a", "grant": "Bearer " + valid},
		map[string]any{"ref": ref, "handle": "handle", "volume": "volume-b", "grant": "Bearer invalid"},
	}}
	encoded, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", bytes.NewReader(encoded)))
	if recorder.Code != http.StatusUnauthorized || opens != 0 {
		t.Fatalf("status=%d opens=%d, want 401 and no mutation", recorder.Code, opens)
	}
	if strings.Contains(recorder.Body.String(), valid) || strings.Contains(recorder.Body.String(), "handle") || strings.Contains(recorder.Body.String(), "volume") {
		t.Fatalf("authorization response leaked sensitive fields: %q", recorder.Body.String())
	}
}

func TestHangarMaterializesWithExactGrantAndSafeSegments(t *testing.T) {
	raw := rawHangarTar(t, "file", "content")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 1}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		return io.NopCloser(bytes.NewReader(canonical)), hangar.TreeAttributes{Ref: ref, LogicalBytes: int64(len(canonical))}, nil
	}
	server, _, key := newHangarTestServer(t, store)
	signer, _ := hangar.NewGrantSigner(key, time.Minute, nil)
	token, _ := signer.Sign(ref, "handle", "volume")
	body, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"ref": ref, "handle": "handle", "volume": "volume", "grant": "Bearer " + token}}})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", bytes.NewReader(body)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(server.storagePath, "steps", "handle", "volume", "file"))
	if err != nil || string(got) != "content" {
		t.Fatalf("materialized bytes=%q err=%v", got, err)
	}
}

func TestHangarRejectsMissingDuplicateAndAlternateGrantsUniformly(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, _, _ := newHangarTestServer(t, store)
	for _, body := range []string{
		`{"items":[{"ref":{"scope":"ci","digest":"` + digest + `","generation":1},"handle":"h","volume":"v"}]}`,
		`{"items":[{"ref":{"scope":"ci","digest":"` + digest + `","generation":1},"handle":"h","volume":"v","grant":"Basic abc"}]}`,
		`{"items":[{"ref":{"scope":"ci","digest":"` + digest + `","generation":1},"handle":"h","volume":"v","grant":"Bearer first","grant":"Bearer second"}]}`,
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(body)))
		if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != "unauthorized\n" {
			t.Fatalf("grant rejection = %d %q, want uniform sanitized 401", recorder.Code, recorder.Body.String())
		}
	}
}

func TestHangarMaterializationRequiresExactCaseSensitiveJSONVocabulary(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, _, _ := newHangarTestServer(t, store)
	validRef := `{"scope":"ci","digest":"` + digest + `","generation":1}`
	for _, body := range []string{
		`{"Items":[]}`,
		`{"items":[{"Ref":` + validRef + `,"handle":"h","volume":"v","grant":"Bearer x"}]}`,
		`{"items":[{"ref":{"Scope":"ci","digest":"` + digest + `","generation":1},"handle":"h","volume":"v","grant":"Bearer x"}]}`,
		`{"items":[{"ref":{"scope":"ci","digest":"` + digest + `","Generation":1},"handle":"h","volume":"v","grant":"Bearer x"}]}`,
		`{"items":[{"ref":` + validRef + `,"handle":"h","volume":"v","grant":"Bearer x","Grant":"Bearer y"}]}`,
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("mixed-case schema = %d %q for %s", recorder.Code, recorder.Body.String(), body)
		}
	}
}

func TestHangarMaterializationRejectsOversizedJSONBeforeAuthorization(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, service, _ := newHangarTestServer(t, store)
	service.MaxControlBytes = 32
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(`{"items":[],"padding":"`+strings.Repeat("x", 64)+`"}`)))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestHangarMaterializationStoreFailureLeavesTargetUntouched(t *testing.T) {
	digest := hangar.Digest("sha256:" + strings.Repeat("b", 64))
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 2}
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		return nil, hangar.TreeAttributes{}, hangar.ErrInfrastructure
	}
	server, _, key := newHangarTestServer(t, store)
	signer, _ := hangar.NewGrantSigner(key, time.Minute, nil)
	token, _ := signer.Sign(ref, "handle", "volume")
	body, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"ref": ref, "handle": "handle", "volume": "volume", "grant": "Bearer " + token}}})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", bytes.NewReader(body)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(server.storagePath, "steps", "handle", "volume")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed strict store left destination visible: %v", err)
	}
}

func TestHangarMaterializationRejectsAbsoluteAndInvalidSegments(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, _, _ := newHangarTestServer(t, store)
	for _, fields := range [][2]string{{"/absolute", "volume"}, {"handle", "../escape"}, {"a/b", "volume"}} {
		body := `{"items":[{"ref":{"scope":"ci","digest":"` + digest + `","generation":1},"handle":"` + fields[0] + `","volume":"` + fields[1] + `","grant":"Bearer opaque"}]}`
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(body)))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("segments %q/%q = %d", fields[0], fields[1], recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(`{"items":[],"destination":"/tmp/escape"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("absolute destination field = %d", recorder.Code)
	}
}

func TestHangarOpenRejectsEveryInvalidRouteSegment(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	store.open = func(context.Context, hangar.TreeRef, int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		panic("invalid route reached store")
	}
	server, _, _ := newHangarTestServer(t, store)
	validDigest := strings.Repeat("a", 64)
	for _, path := range []string{
		"/hangar/v1/scopes/INVALID/trees/sha256/" + validDigest + "/generations/1",
		"/hangar/v1/scopes/ci/trees/sha256/" + strings.Repeat("A", 64) + "/generations/1",
		"/hangar/v1/scopes/ci/trees/sha256/" + validDigest[:63] + "/generations/1",
		"/hangar/v1/scopes/ci/trees/sha256/" + validDigest + "/generations/0",
		"/hangar/v1/scopes/ci/trees/sha256/" + validDigest + "/generations/01",
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, recorder.Code)
		}
	}
}

func TestHangarInterruptedPublishNeverCallsStore(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("interrupted request reached store")
	}}
	server, _, _ := newHangarTestServer(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", bytes.NewReader(rawHangarTar(t, "x", "y"))).WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

type cancelingBody struct {
	cancel context.CancelFunc
	sent   bool
}

func (body *cancelingBody) Read(buffer []byte) (int, error) {
	if body.sent {
		return 0, context.Canceled
	}
	body.sent = true
	copy(buffer, []byte("partial tar bytes"))
	body.cancel()
	return len("partial tar bytes"), nil
}
func (*cancelingBody) Close() error { return nil }

func TestHangarInterruptedBodyStopsBeforeStoreMutation(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("interrupted body reached store")
	}}
	server, _, _ := newHangarTestServer(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", nil).WithContext(ctx)
	req.Body = &cancelingBody{cancel: cancel}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestHangarAuthorizationFailureDoesNotLeakTokenOrFieldsToResponseOrLogs(t *testing.T) {
	logger := lagertest.NewTestLogger("hangar-secret-test")
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected")
	}}
	server, _, _ := newHangarTestServerWithLogger(t, store, logger)
	secret := "Bearer secret-token-value"
	body := `{"items":[{"ref":{"scope":"opaque-scope","digest":"sha256:` + strings.Repeat("d", 64) + `","generation":1},"handle":"secret-handle","volume":"secret-volume","grant":"` + secret + `"}]}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", strings.NewReader(body)))
	combined := recorder.Body.String() + string(logger.Buffer().Contents())
	for _, forbidden := range []string{"secret-token-value", "opaque-scope", "secret-handle", "secret-volume"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("authorization failure leaked %q: %s", forbidden, combined)
		}
	}
}

func TestHangarStrictFailureLeavesLegacyCacheRoutesFailOpen(t *testing.T) {
	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		return hangar.TreeAttributes{}, false, hangar.ErrInfrastructure
	}}
	server, _, _ := newHangarTestServer(t, store)
	server.SetDurableTier(NewDurableTier(lagertest.NewTestLogger("broken-tier"), brokenStore{}, server.Metrics(), time.Minute))
	for _, request := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodHead, "/resource-caches/missing", "", http.StatusNotFound},
		{http.MethodGet, "/resource-caches/missing", "", http.StatusNotFound},
		{http.MethodPost, "/durable/restore", `{"key":"missing","durable_key":"resource-caches/missing"}`, http.StatusNotFound},
		{http.MethodPost, "/hangar/v1/scopes/ci/trees", string(rawHangarTar(t, "x", "y")), http.StatusServiceUnavailable},
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		if recorder.Code != request.want {
			t.Errorf("%s %s = %d, want %d", request.method, request.path, recorder.Code, request.want)
		}
	}
}

func TestHangarConcurrentIdenticalPublish(t *testing.T) {
	raw := rawHangarTar(t, "x", "same")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	var mu sync.Mutex
	created := false
	store := &hangarStoreStub{}
	store.ensure = func(_ context.Context, scope hangar.Scope, got hangar.Digest, source io.Reader, _ int64) (hangar.TreeAttributes, bool, error) {
		body, _ := io.ReadAll(source)
		if got != digest || !bytes.Equal(body, canonical) {
			t.Errorf("noncanonical publish")
		}
		mu.Lock()
		wasCreated := !created
		created = true
		mu.Unlock()
		return hangar.TreeAttributes{Ref: hangar.TreeRef{Scope: scope, Digest: got, Generation: 1}, LogicalBytes: int64(len(body))}, wasCreated, nil
	}
	server, _, _ := newHangarTestServer(t, store)
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", bytes.NewReader(raw)))
			statuses <- recorder.Code
		}()
	}
	a, b := <-statuses, <-statuses
	if !((a == 200 && b == 201) || (a == 201 && b == 200)) {
		t.Fatalf("statuses=(%d,%d)", a, b)
	}
}

func TestHangarConfigRequiresStrictPrerequisitesBeforeStoreConstruction(t *testing.T) {
	base := hangarOptions{
		Enabled: true, ScratchDir: filepath.Join(t.TempDir(), "scratch"), CapabilityKey: filepath.Join(t.TempDir(), "key"),
		MaxContentBytes: 1024, MaxEntries: 10, CapabilityTTL: 15 * time.Minute, DurableKind: "gcs", Bucket: "bucket", Timeout: time.Minute,
		TLSCert: "cert", TLSKey: "key", TLSCACert: "ca",
	}
	if err := os.WriteFile(base.CapabilityKey, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*hangarOptions)
	}{
		{"partial-tls", func(o *hangarOptions) { o.TLSCACert = "" }},
		{"non-gcs", func(o *hangarOptions) { o.DurableKind = "filesystem" }},
		{"empty-bucket", func(o *hangarOptions) { o.Bucket = "" }},
		{"relative-scratch", func(o *hangarOptions) { o.ScratchDir = "relative" }},
		{"bad-key", func(o *hangarOptions) {
			o.CapabilityKey = filepath.Join(t.TempDir(), "short")
			_ = os.WriteFile(o.CapabilityKey, []byte("short"), 0600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.mutate(&opts)
			if err := validateHangarOptions(opts, t.TempDir()); err == nil {
				t.Fatal("accepted invalid Hangar configuration")
			}
		})
	}
}

func TestHangarConfigRejectsCapabilityTTLOutsideCoreBound(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "hangar.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte("k"), 32), 0600); err != nil {
		t.Fatal(err)
	}
	base := hangarOptions{
		Enabled: true, ScratchDir: filepath.Join(t.TempDir(), "scratch"), CapabilityKey: keyPath,
		MaxContentBytes: 1024, MaxEntries: 10, CapabilityTTL: 15 * time.Minute,
		DurableKind: "gcs", Bucket: "bucket", Timeout: time.Minute,
		TLSCert: "cert", TLSKey: "key", TLSCACert: "ca",
	}
	for _, ttl := range []time.Duration{0, -time.Second, hangar.MaxGrantTTL + time.Nanosecond} {
		opts := base
		opts.CapabilityTTL = ttl
		if err := validateHangarOptions(opts, filepath.Join(t.TempDir(), "artifacts")); err == nil || !strings.Contains(err.Error(), "hangar-capability-ttl") {
			t.Fatalf("TTL %s: got %v, want bounded capability TTL error", ttl, err)
		}
	}
}

func TestHangarAndDurableGCSShareRootEndpointAndValidateBucket(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	fakeGCS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/storage/v1/b/bucket" {
			_, _ = w.Write([]byte(`{"name":"bucket"}`))
			return
		}
		if r.URL.Path == "/bucket/resource-caches/shared" {
			w.Header().Set("Content-Length", "3")
			_, _ = w.Write([]byte("obj"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"missing"}}`))
	}))
	defer fakeGCS.Close()
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "scratch")
	service, closeService, err := buildHangarService(context.Background(), lagertest.NewTestLogger("hangar-build"), storage, hangarOptions{
		Enabled: true, ScratchDir: scratch, CapabilityKey: keyPath, MaxContentBytes: 1024, MaxEntries: 10, CapabilityTTL: 15 * time.Minute,
		DurableKind: "gcs", Bucket: "bucket", Endpoint: fakeGCS.URL, Timeout: time.Second,
		TLSCert: "cert", TLSKey: "key", TLSCACert: "ca",
	})
	if err != nil {
		t.Fatalf("build Hangar: %v", err)
	}
	if service == nil {
		t.Fatal("Hangar service disabled unexpectedly")
	}
	if err := closeService(); err != nil {
		t.Fatal(err)
	}
	durableStore, err := durable.NewGCS(context.Background(), durable.GCSConfig{Bucket: "bucket", Endpoint: fakeGCS.URL})
	if err != nil {
		t.Fatalf("build durable: %v", err)
	}
	reader, found, err := durableStore.Get(context.Background(), "resource-caches/shared")
	if err != nil || !found {
		mu.Lock()
		gotPaths := append([]string(nil), paths...)
		mu.Unlock()
		t.Fatalf("durable get found=%v err=%v paths=%v", found, err, gotPaths)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(body) != "obj" {
		t.Fatalf("durable get body=%q read=%v close=%v", body, readErr, closeErr)
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	wantPaths := []string{
		"GET /storage/v1/b/bucket",
		"GET /bucket/resource-caches%2Fshared",
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("shared endpoint paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestHangarScratchRejectsSymlinkWithoutChangingItsTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "scratch-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateHangarScratch(link, t.TempDir()); err == nil {
		t.Fatal("accepted symlink scratch directory")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("rejected symlink changed target mode to %04o", info.Mode().Perm())
	}
}

func TestHangarScratchPathsRejectRootsAndContainmentBeforeMutation(t *testing.T) {
	for _, tc := range []struct{ scratch, storage string }{
		{"/", filepath.Join(t.TempDir(), "storage")},
		{filepath.Join(t.TempDir(), "scratch"), "/"},
	} {
		// The first two cases prove roots are rejected by the pure path gate;
		// no chmod or mkdir is reachable from this function.
		if err := validateHangarScratchPaths(tc.scratch, tc.storage); err == nil {
			t.Errorf("accepted scratch=%q storage=%q", tc.scratch, tc.storage)
		}
	}
	base := t.TempDir()
	if err := validateHangarScratchPaths(filepath.Join(base, "scratch"), filepath.Join(base, "storage")); err != nil {
		t.Fatalf("rejected disjoint siblings: %v", err)
	}
	if err := validateHangarScratchPaths(filepath.Join(base, "storage", "scratch"), filepath.Join(base, "storage")); err == nil {
		t.Fatal("accepted scratch contained by storage")
	}
	if err := validateHangarScratchPaths(filepath.Join(base, "storage"), filepath.Join(base, "storage", "steps")); err == nil {
		t.Fatal("accepted storage contained by scratch")
	}
}

func TestHangarRootRejectionLeavesFilesystemRootModeUnchanged(t *testing.T) {
	before, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateHangarScratch(string(filepath.Separator), filepath.Join(t.TempDir(), "storage")); err == nil {
		t.Fatal("accepted filesystem root")
	}
	after, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("root mode changed from %04o to %04o", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestHangarReadinessLabelCannotCollideWithLegacyLabel(t *testing.T) {
	if err := validateDaemonLabelKeys(HangarReadyLabel); err == nil {
		t.Fatal("accepted Hangar readiness as legacy label key")
	}
	if err := validateDaemonLabelKeys("concourse.dev/artifact-cache"); err != nil {
		t.Fatalf("rejected distinct label: %v", err)
	}
}

func TestHangarLabelCollisionClearsStaleReadinessBeforeRejection(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{HangarReadyLabel: "ready"}}})
	hangarLabeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	legacyLabeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", HangarReadyLabel)
	if err := prepareDaemonLabels(context.Background(), HangarReadyLabel, hangarLabeler, legacyLabeler); err == nil {
		t.Fatal("accepted colliding label configuration")
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if _, found := node.Labels[HangarReadyLabel]; found {
		t.Fatal("stale Hangar readiness survived colliding startup")
	}
}

func TestHangarLabelPreparationFailureRetriesCentralCleanup(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{HangarReadyLabel: "ready", "concourse.dev/artifact-cache": "ready"}}})
	patches := 0
	client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patches++
		if patches == 1 {
			return true, nil, errors.New("transient patch failure")
		}
		return false, nil, nil
	})
	hangarLabeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	legacyLabeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", "concourse.dev/artifact-cache")
	if err := prepareDaemonLabels(context.Background(), "concourse.dev/artifact-cache", hangarLabeler, legacyLabeler); err == nil {
		t.Fatal("ignored preparation failure")
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if len(node.Labels) != 0 || patches < 3 {
		t.Fatalf("cleanup retry patches=%d labels=%v", patches, node.Labels)
	}
}

type contextAwareNodeClient struct {
	kubernetes.Interface
	nodes typedcorev1.NodeInterface
}

func (client *contextAwareNodeClient) CoreV1() typedcorev1.CoreV1Interface {
	return &contextAwareCoreClient{CoreV1Interface: client.Interface.CoreV1(), nodes: client.nodes}
}

type contextAwareCoreClient struct {
	typedcorev1.CoreV1Interface
	nodes typedcorev1.NodeInterface
}

func (client *contextAwareCoreClient) Nodes() typedcorev1.NodeInterface { return client.nodes }

type expiringFirstPatchNodes struct {
	typedcorev1.NodeInterface
	preparation context.Context
	cancel      context.CancelFunc
	patches     int
	cleanupRuns int
	cleanupLive bool
	cleanupNew  bool
	bounded     bool
}

func (nodes *expiringFirstPatchNodes) Patch(ctx context.Context, name string, patchType types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*corev1.Node, error) {
	nodes.patches++
	if nodes.patches == 1 {
		nodes.cancel()
		return nil, context.DeadlineExceeded
	}
	nodes.cleanupRuns++
	nodes.cleanupLive = nodes.cleanupLive && ctx.Err() == nil
	nodes.cleanupNew = nodes.cleanupNew && ctx != nodes.preparation
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		nodes.bounded = nodes.bounded && remaining > 0 && remaining <= daemonLabelCleanupTimeout
	} else {
		nodes.bounded = false
	}
	return nodes.NodeInterface.Patch(ctx, name, patchType, data, options, subresources...)
}

func TestHangarLabelPreparationFailureUsesFreshBoundedCleanupContext(t *testing.T) {
	base := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{HangarReadyLabel: "ready"}}})
	preparation, cancel := context.WithCancel(context.Background())
	nodes := &expiringFirstPatchNodes{
		NodeInterface: base.CoreV1().Nodes(), preparation: preparation, cancel: cancel,
		cleanupLive: true, cleanupNew: true, bounded: true,
	}
	client := &contextAwareNodeClient{Interface: base, nodes: nodes}
	hangarLabeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	legacyLabeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", "concourse.dev/artifact-cache")
	if err := prepareDaemonLabels(preparation, "concourse.dev/artifact-cache", hangarLabeler, legacyLabeler); err == nil {
		t.Fatal("ignored expired preparation context")
	}
	node, _ := base.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if _, found := node.Labels[HangarReadyLabel]; found {
		t.Fatal("stale readiness survived cleanup after preparation deadline")
	}
	if nodes.cleanupRuns != 2 || !nodes.cleanupLive || !nodes.cleanupNew || !nodes.bounded {
		t.Fatalf("cleanup runs=%d context live=%v new=%v bounded=%v patches=%d", nodes.cleanupRuns, nodes.cleanupLive, nodes.cleanupNew, nodes.bounded, nodes.patches)
	}
}

func TestHangarDisabledStartupClearsStaleReadinessAndKeepsLegacyReady(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{HangarReadyLabel: "ready"}}})
	hangarLabeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	legacyLabeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", "concourse.dev/artifact-cache")
	if err := prepareDaemonLabels(context.Background(), "concourse.dev/artifact-cache", hangarLabeler, legacyLabeler); err != nil {
		t.Fatal(err)
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if _, found := node.Labels[HangarReadyLabel]; found {
		t.Fatal("stale Hangar readiness survived disabled startup")
	}
	if node.Labels["concourse.dev/artifact-cache"] != "ready" {
		t.Fatal("legacy readiness was not preserved")
	}
}

type hangarListenerStub struct{ closed bool }

func (*hangarListenerStub) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (listener *hangarListenerStub) Close() error     { listener.closed = true; return nil }
func (*hangarListenerStub) Addr() net.Addr            { return &net.TCPAddr{} }

func TestHangarReadinessIsNotAddedWhenListenerBindFails(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}})
	labeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	_, err := listenAndAdvertiseHangar(context.Background(), ":0", labeler, func(string, string) (net.Listener, error) { return nil, errors.New("bind failed") })
	if err == nil {
		t.Fatal("bind failure was ignored")
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if _, found := node.Labels[HangarReadyLabel]; found {
		t.Fatal("readiness was added before listener bind")
	}
}

func TestHangarReadinessIsAddedOnlyAfterListenerExists(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}})
	labeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	stub := &hangarListenerStub{}
	bound := false
	listener, err := listenAndAdvertiseHangar(context.Background(), ":0", labeler, func(string, string) (net.Listener, error) { bound = true; return stub, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !bound {
		t.Fatal("label path ran without binding")
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if node.Labels[HangarReadyLabel] != "ready" {
		t.Fatal("bound listener did not advertise readiness")
	}
}

func TestHangarCleanupRemovesLabelsThenClosesClient(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{HangarReadyLabel: "ready", "concourse.dev/artifact-cache": "ready"}}})
	var order []string
	client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var patch struct {
			Metadata struct {
				Labels map[string]any `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(action.(k8stesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		for key := range patch.Metadata.Labels {
			order = append(order, key)
		}
		return false, nil, nil
	})
	hangarLabeler := NewNodeLabeler(lagertest.NewTestLogger("hangar-label"), client, "node", HangarReadyLabel)
	legacyLabeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", "concourse.dev/artifact-cache")
	if err := cleanupDaemonServices(context.Background(), hangarLabeler, legacyLabeler, func() error { order = append(order, "shutdown"); return nil }, func() error { order = append(order, "close"); return nil }); err != nil {
		t.Fatal(err)
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	wantOrder := []string{HangarReadyLabel, "concourse.dev/artifact-cache", "shutdown", "close"}
	if len(node.Labels) != 0 || !slices.Equal(order, wantOrder) {
		t.Fatalf("cleanup left labels=%v order=%v, want %v", node.Labels, order, wantOrder)
	}
}
