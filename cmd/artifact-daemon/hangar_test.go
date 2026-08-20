package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

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
	server, err := NewServer(lagertest.NewTestLogger("hangar-daemon"), storage, "node-a")
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
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Ref.Generation != 7 || got.Ref.Digest != digest || got.Ref.Scope != "ci" || got.StoredBytes != 91 || got.LogicalBytes != int64(len(canonical)) || !got.CreatedAt.Equal(createdAt) {
			t.Fatalf("attributes = %#v", got)
		}
	}
}

type failingReadCloser struct{ closeErr error }

func (*failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("corrupt after open") }
func (reader *failingReadCloser) Close() error      { return reader.closeErr }

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
		MaxContentBytes: 1024, MaxEntries: 10, DurableKind: "gcs", Bucket: "bucket", Timeout: time.Minute,
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
