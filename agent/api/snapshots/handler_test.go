package snapshots_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/repodiff"
	"github.com/concourse/concourse/agent/snapshot"
)

type fakeCreator struct {
	upload func(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error)
	calls  []snapshot.UploadRequest
}

func (fake *fakeCreator) Upload(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
	fake.calls = append(fake.calls, request.Clone())
	if fake.upload == nil {
		return snapshot.Snapshot{}, errors.New("unexpected upload")
	}
	return fake.upload(ctx, request)
}

type fakeMetadata struct {
	get         func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
	detail      func(context.Context, int, snapshot.SnapshotID) (snapshot.Detail, bool, error)
	list        func(context.Context, int, snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error)
	pin         func(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef, string) (snapshot.RetentionClaim, error)
	unpin       func(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef) error
	getCalls    []authorizedCall
	detailCalls []authorizedCall
	listCalls   []listCall
	pinCalls    []pinCall
	unpinCalls  []unpinCall
}

type authorizedCall struct {
	teamID int
	id     snapshot.SnapshotID
}

type listCall struct {
	teamID int
	filter snapshot.SnapshotListFilter
}

type pinCall struct {
	lease  snapshot.DigestLease
	teamID int
	actor  string
	ref    snapshot.SnapshotRef
	reason string
}

type unpinCall struct {
	lease  snapshot.DigestLease
	teamID int
	actor  string
	ref    snapshot.SnapshotRef
}

func (fake *fakeMetadata) GetAuthorized(ctx context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	fake.getCalls = append(fake.getCalls, authorizedCall{teamID: teamID, id: id})
	if fake.get == nil {
		return snapshot.Snapshot{}, false, errors.New("unexpected GetAuthorized")
	}
	return fake.get(ctx, teamID, id)
}

func (fake *fakeMetadata) GetAuthorizedDetail(ctx context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Detail, bool, error) {
	fake.detailCalls = append(fake.detailCalls, authorizedCall{teamID: teamID, id: id})
	if fake.detail == nil {
		return snapshot.Detail{}, false, errors.New("unexpected GetAuthorizedDetail")
	}
	return fake.detail(ctx, teamID, id)
}

func (fake *fakeMetadata) ListAuthorized(ctx context.Context, teamID int, filter snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error) {
	fake.listCalls = append(fake.listCalls, listCall{teamID: teamID, filter: filter.Clone()})
	if fake.list == nil {
		return nil, errors.New("unexpected ListAuthorized")
	}
	return fake.list(ctx, teamID, filter)
}

func (fake *fakeMetadata) Pin(ctx context.Context, lease snapshot.DigestLease, teamID int, actor string, ref snapshot.SnapshotRef, reason string) (snapshot.RetentionClaim, error) {
	fake.pinCalls = append(fake.pinCalls, pinCall{lease: lease, teamID: teamID, actor: actor, ref: ref, reason: reason})
	if fake.pin == nil {
		return snapshot.RetentionClaim{}, errors.New("unexpected Pin")
	}
	return fake.pin(ctx, lease, teamID, actor, ref, reason)
}

func (fake *fakeMetadata) Unpin(ctx context.Context, lease snapshot.DigestLease, teamID int, actor string, ref snapshot.SnapshotRef) error {
	fake.unpinCalls = append(fake.unpinCalls, unpinCall{lease: lease, teamID: teamID, actor: actor, ref: ref})
	if fake.unpin == nil {
		return errors.New("unexpected Unpin")
	}
	return fake.unpin(ctx, lease, teamID, actor, ref)
}

type fakeContent struct {
	open      func(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
	openCalls []snapshot.Snapshot
}

type fakeRepositoryChanges struct {
	get   func(context.Context, snapshot.SnapshotID) (projection.RepositoryChange, bool, error)
	calls []snapshot.SnapshotID
}

func (fake *fakeRepositoryChanges) GetRepositoryChangeProjection(ctx context.Context, id snapshot.SnapshotID) (projection.RepositoryChange, bool, error) {
	fake.calls = append(fake.calls, id)
	if fake.get == nil {
		return projection.RepositoryChange{}, false, errors.New("unexpected repository-change projection read")
	}
	return fake.get(ctx, id)
}

type trackedReadCloser struct {
	io.Reader
	closed   int
	closeErr error
}

type lateErrorReader struct {
	payload []byte
	err     error
	sent    bool
}

func (reader *lateErrorReader) Read(destination []byte) (int, error) {
	if !reader.sent {
		reader.sent = true
		return copy(destination, reader.payload), nil
	}
	return 0, reader.err
}

func (reader *trackedReadCloser) Close() error {
	reader.closed++
	return reader.closeErr
}

func (fake *fakeContent) Open(ctx context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
	fake.openCalls = append(fake.openCalls, manifest.Clone())
	if fake.open == nil {
		return nil, errors.New("unexpected Open")
	}
	return fake.open(ctx, manifest)
}

type fakeLease struct {
	digest   snapshot.Digest
	closed   int
	covers   bool
	closeErr error
}

func (lease *fakeLease) Covers(digest snapshot.Digest) bool {
	return lease.covers && digest == lease.digest
}

func (lease *fakeLease) Close() error {
	lease.closed++
	return lease.closeErr
}

type fakeLocks struct {
	acquire func(context.Context, []snapshot.Digest) (snapshot.DigestLease, error)
	calls   [][]snapshot.Digest
}

func (fake *fakeLocks) AcquireMany(ctx context.Context, digests []snapshot.Digest) (snapshot.DigestLease, error) {
	fake.calls = append(fake.calls, append([]snapshot.Digest(nil), digests...))
	if fake.acquire == nil {
		return nil, errors.New("unexpected AcquireMany")
	}
	return fake.acquire(ctx, digests)
}

type handlerHarness struct {
	factory  *snapshotsapi.HandlerFactory
	creator  *fakeCreator
	metadata *fakeMetadata
	content  *fakeContent
	changes  *fakeRepositoryChanges
	locks    *fakeLocks
	team     snapshotsapi.TrustedTeam
	manifest snapshot.Snapshot
	reports  []string
}

func newHandlerHarness(t *testing.T) *handlerHarness {
	t.Helper()
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	payloadDigest := sha256.Sum256([]byte("tar"))
	manifest := snapshot.Snapshot{
		ID: 9007199254740993, Type: snapshot.TypeRef("opaque/v1"),
		Digest:   snapshot.Digest("sha256:" + hex.EncodeToString(payloadDigest[:])),
		ByteSize: 3, FileCount: 1, Representation: "application/x-tar",
		IntrinsicMetadata: json.RawMessage(`{"kind":"opaque"}`),
		ContentState:      snapshot.ContentStateAvailable, CreatedAt: now,
	}
	creator := &fakeCreator{}
	metadata := &fakeMetadata{}
	content := &fakeContent{}
	changes := &fakeRepositoryChanges{}
	locks := &fakeLocks{}
	harness := &handlerHarness{
		creator: creator, metadata: metadata, content: content, changes: changes, locks: locks,
		team: snapshotsapi.TrustedTeam{ID: 7, Name: "main"}, manifest: manifest,
	}
	factory, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{
		Enabled: true, Creator: creator, Metadata: metadata, Content: content, Locks: locks,
		RepositoryChanges: changes,
		ArchiveLimits:     snapshot.ArchiveLimits{MaxContentBytes: 64, MaxEntries: 4},
		TempDir:           t.TempDir(),
		Identity: func(*http.Request) (snapshotsapi.RequestIdentity, error) {
			return snapshotsapi.RequestIdentity{Actor: "github:subject-1", DisplayName: "Alice"}, nil
		},
		ReportError: func(_ context.Context, category string) { harness.reports = append(harness.reports, category) },
	})
	if err != nil {
		t.Fatalf("construct enabled handler factory: %v", err)
	}
	harness.factory = factory
	return harness
}

func TestRepositoryChangeProjectionIsTeamScopedAndReturnsDurableDiff(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.manifest.Type = "repository-change/v1"
	harness.metadata.get = func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if teamID != harness.team.ID || id != harness.manifest.ID {
			t.Fatalf("authorization lookup = team %d snapshot %s", teamID, id)
		}
		return harness.manifest, true, nil
	}
	harness.changes.get = func(_ context.Context, id snapshot.SnapshotID) (projection.RepositoryChange, bool, error) {
		return projection.RepositoryChange{
			SnapshotID: id, Status: projection.RepositoryChangeProjectionReady,
			RepositoryID: "sha256:" + strings.Repeat("b", 64), BaseSHA: strings.Repeat("c", 40),
			ResultTreeSHA: strings.Repeat("d", 40), Representation: "patch",
			Files:     []repodiff.ChangedFile{{Path: "README.md", Status: repodiff.ChangeModified, LinesAdded: 1}},
			FileCount: 1, LinesAdded: 1, UnifiedDiff: "diff --git a/README.md b/README.md\n",
		}, true, nil
	}
	recorder := httptest.NewRecorder()
	harness.factory.RepositoryChangeProjection(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodGet, "/projection", harness.manifest.ID.String(), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response projection.RepositoryChange
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != projection.RepositoryChangeProjectionReady || response.FileCount != 1 || response.Files[0].Path != "README.md" {
		t.Fatalf("response = %#v", response)
	}
	if len(harness.changes.calls) != 1 || harness.changes.calls[0] != harness.manifest.ID {
		t.Fatalf("projection calls = %#v", harness.changes.calls)
	}
}

func TestRepositoryChangeProjectionDoesNotCrossTeamAuthorizationBoundary(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return snapshot.Snapshot{}, false, nil
	}
	recorder := httptest.NewRecorder()
	harness.factory.RepositoryChangeProjection(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodGet, "/projection", harness.manifest.ID.String(), nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(harness.changes.calls) != 0 {
		t.Fatalf("unauthorized request queried global projection: %#v", harness.changes.calls)
	}
}

func TestRepositoryChangeProjectionReportsPendingWhileReconciliationHasNoRow(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.manifest.Type = "repository-change/v1"
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	harness.changes.get = func(context.Context, snapshot.SnapshotID) (projection.RepositoryChange, bool, error) {
		return projection.RepositoryChange{}, false, nil
	}
	recorder := httptest.NewRecorder()
	harness.factory.RepositoryChangeProjection(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodGet, "/projection", harness.manifest.ID.String(), nil))
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), `"status":"pending"`) {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func snapshotRequest(method, path, id string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, path, body)
	if id != "" {
		query := request.URL.Query()
		query.Set(":snapshot_id", id)
		request.URL.RawQuery = query.Encode()
	}
	return request
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) snapshotsapi.ErrorResponse {
	t.Helper()
	var response snapshotsapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestCreateUsesTrustedAuthorityAndStreamsBoundedTar(t *testing.T) {
	harness := newHandlerHarness(t)
	body := &trackedReadCloser{Reader: strings.NewReader("tar")}
	openCalls := 0
	harness.creator.upload = func(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
		openCalls++
		reader, err := request.OpenTar(ctx)
		if err != nil {
			return snapshot.Snapshot{}, err
		}
		got, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return snapshot.Snapshot{}, errors.Join(err, closeErr)
		}
		if string(got) != "tar" {
			t.Fatalf("uploaded body = %q", got)
		}
		if _, err := request.OpenTar(ctx); err == nil {
			t.Fatal("request body opener was reusable")
		}
		return harness.manifest, nil
	}

	request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", body)
	request.Header.Set("Content-Type", "application/x-tar")
	request.Header.Set("Idempotency-Key", "upload-1")
	recorder := httptest.NewRecorder()
	harness.factory.Create(harness.team).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	if !strings.Contains(recorder.Body.String(), `"id":"9007199254740993"`) {
		t.Fatalf("response lost exact ID: %s", recorder.Body.String())
	}
	if len(harness.creator.calls) != 1 || openCalls != 1 {
		t.Fatalf("upload calls = %d, open calls = %d", len(harness.creator.calls), openCalls)
	}
	got := harness.creator.calls[0]
	if got.TeamID != 7 || got.TeamName != "main" || got.UploadedBy != "Alice" ||
		got.Actor != "github:subject-1" || got.Type != snapshot.TypeRef("opaque/v1") ||
		got.IdempotencyKey != "upload-1" {
		t.Fatalf("upload authority/request = %#v", got)
	}
	if string(got.SourceMetadata) != `{"adapter":"upload","uploader":"Alice"}` {
		t.Fatalf("source metadata = %s", got.SourceMetadata)
	}
	if body.closed != 1 {
		t.Fatalf("request body closes = %d, want 1", body.closed)
	}
}

func TestCreateGeneratesAnUploadKeyWhenHeaderIsAbsent(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.creator.upload = func(_ context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
		reader, err := request.OpenTar(context.Background())
		if err != nil {
			return snapshot.Snapshot{}, err
		}
		_, readErr := io.Copy(io.Discard, reader)
		return harness.manifest, errors.Join(readErr, reader.Close())
	}
	request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", strings.NewReader("tar"))
	request.Header.Set("Content-Type", "application/x-tar")
	recorder := httptest.NewRecorder()
	harness.factory.Create(harness.team).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(harness.creator.calls) != 1 || !strings.HasPrefix(harness.creator.calls[0].IdempotencyKey, "http-") {
		t.Fatalf("generated key = %q", harness.creator.calls[0].IdempotencyKey)
	}
}

func TestCreateRejectsMalformedTransportBeforeCallingCreator(t *testing.T) {
	limits := snapshot.ArchiveLimits{MaxContentBytes: 64, MaxEntries: 4}
	transportLimit, err := limits.CanonicalArchiveByteLimit()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		path          string
		contentType   []string
		encoding      []string
		keys          []string
		contentLength int64
		status        int
	}{
		{name: "missing type", path: "/snapshots", contentType: []string{"application/x-tar"}, status: 400},
		{name: "multiple type", path: "/snapshots?type=opaque%2Fv1&type=review%2Fv1", contentType: []string{"application/x-tar"}, status: 400},
		{name: "invalid type", path: "/snapshots?type=Opaque", contentType: []string{"application/x-tar"}, status: 400},
		{name: "unknown query", path: "/snapshots?type=opaque%2Fv1&team_id=9", contentType: []string{"application/x-tar"}, status: 400},
		{name: "malformed query", path: "/snapshots?type=opaque%2Fv1&team_id;=9", contentType: []string{"application/x-tar"}, status: 400},
		{name: "missing media type", path: "/snapshots?type=opaque%2Fv1", status: 415},
		{name: "media parameters", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar; charset=utf-8"}, status: 415},
		{name: "multiple media values", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar", "application/x-tar"}, status: 415},
		{name: "encoded body", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar"}, encoding: []string{"identity"}, status: 415},
		{name: "multiple keys", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar"}, keys: []string{"a", "b"}, status: 400},
		{name: "noncanonical key", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar"}, keys: []string{" key "}, status: 400},
		{name: "declared too large", path: "/snapshots?type=opaque%2Fv1", contentType: []string{"application/x-tar"}, contentLength: transportLimit + 1, status: 413},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader("tar"))
			if test.contentType != nil {
				request.Header["Content-Type"] = test.contentType
			}
			if test.encoding != nil {
				request.Header["Content-Encoding"] = test.encoding
			}
			if test.keys != nil {
				request.Header["Idempotency-Key"] = test.keys
			}
			if test.contentLength != 0 {
				request.ContentLength = test.contentLength
			}
			recorder := httptest.NewRecorder()
			harness.factory.Create(harness.team).ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			if len(harness.creator.calls) != 0 {
				t.Fatalf("creator called %d times", len(harness.creator.calls))
			}
			response := decodeError(t, recorder)
			if response.Error == "" || response.Message == "" || len(response.Message) > 256 {
				t.Fatalf("error response = %#v", response)
			}
		})
	}
}

func TestCreateMapsStreamLimitAndDomainErrorsWithoutLeakingDetails(t *testing.T) {
	t.Run("stream limit", func(t *testing.T) {
		harness := newHandlerHarness(t)
		limit, err := (snapshot.ArchiveLimits{MaxContentBytes: 64, MaxEntries: 4}).CanonicalArchiveByteLimit()
		if err != nil {
			t.Fatal(err)
		}
		harness.creator.upload = func(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
			reader, err := request.OpenTar(ctx)
			if err != nil {
				return snapshot.Snapshot{}, err
			}
			_, readErr := io.Copy(io.Discard, reader)
			return snapshot.Snapshot{}, errors.Join(readErr, reader.Close())
		}
		request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", bytes.NewReader(make([]byte, limit+1)))
		request.Header.Set("Content-Type", "application/x-tar")
		request.ContentLength = -1
		recorder := httptest.NewRecorder()
		harness.factory.Create(harness.team).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusRequestEntityTooLarge || decodeError(t, recorder).Error != "limit_exceeded" {
			t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid archive", err: snapshot.ErrInvalidArchive, status: 400, code: "invalid_archive"},
		{name: "limit", err: snapshot.ErrLimitExceeded, status: 413, code: "limit_exceeded"},
		{name: "unsupported type", err: snapshot.ErrUnsupportedType, status: 400, code: "invalid_type"},
		{name: "semantic validation", err: snapshot.ErrValidation, status: 422, code: "validation_failed"},
		{name: "conflict", err: snapshot.ErrConflict, status: 409, code: "conflict"},
		{name: "unavailable", err: snapshot.ErrContentUnavailable, status: 503, code: "content_unavailable"},
		{name: "unexpected", err: errors.New("platform"), status: 500, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			harness.creator.upload = func(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
				return snapshot.Snapshot{}, errors.Join(test.err, errors.New("secret /tmp/storage-node"))
			}
			request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", strings.NewReader("tar"))
			request.Header.Set("Content-Type", "application/x-tar")
			recorder := httptest.NewRecorder()
			harness.factory.Create(harness.team).ServeHTTP(recorder, request)
			response := decodeError(t, recorder)
			if recorder.Code != test.status || response.Error != test.code {
				t.Fatalf("status/error = %d/%q, want %d/%q", recorder.Code, response.Error, test.status, test.code)
			}
			if strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "/tmp") {
				t.Fatalf("response leaked dependency error: %s", recorder.Body.String())
			}
		})
	}
}

func TestListMapsStrictFiltersAndPreservesExactIDs(t *testing.T) {
	harness := newHandlerHarness(t)
	createdAfter, err := time.Parse(time.RFC3339, "2026-07-21T01:02:03-07:00")
	if err != nil {
		t.Fatal(err)
	}
	harness.metadata.list = func(_ context.Context, _ int, _ snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error) {
		return []snapshot.Snapshot{harness.manifest}, nil
	}
	query := url.Values{
		"type":          {"opaque/v1"},
		"content_state": {"available"},
		"created_after": {createdAfter.Format(time.RFC3339)},
		"limit":         {"17"},
	}
	request := httptest.NewRequest(http.MethodGet, "/snapshots?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(harness.metadata.listCalls) != 1 {
		t.Fatalf("list calls = %d", len(harness.metadata.listCalls))
	}
	gotCall := harness.metadata.listCalls[0]
	if gotCall.teamID != 7 || gotCall.filter.Type != snapshot.TypeRef("opaque/v1") ||
		gotCall.filter.ContentState != snapshot.ContentStateAvailable || gotCall.filter.Limit != 18 ||
		gotCall.filter.CreatedAfter == nil || !gotCall.filter.CreatedAfter.Equal(createdAfter) {
		t.Fatalf("list call = %#v", gotCall)
	}
	if !strings.Contains(recorder.Body.String(), `"id":"9007199254740993"`) {
		t.Fatalf("response lost exact ID: %s", recorder.Body.String())
	}
}

func TestListUsesDefaultLimitAndEncodesEmptyResultsAsArray(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.list = func(context.Context, int, snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error) {
		return nil, nil
	}
	recorder := httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/snapshots", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "[]" {
		t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
	}
	if len(harness.metadata.listCalls) != 1 || harness.metadata.listCalls[0].filter.Limit != 101 {
		t.Fatalf("list calls = %#v", harness.metadata.listCalls)
	}
}

func TestListUsesAnOpaqueExclusiveCursorAndPreservesFiltersInTheNextLink(t *testing.T) {
	harness := newHandlerHarness(t)
	createdAt := time.Date(2026, time.July, 22, 12, 0, 0, 123456000, time.UTC)
	values := make([]snapshot.Snapshot, 3)
	for index, id := range []snapshot.SnapshotID{30, 29, 28} {
		values[index] = harness.manifest.Clone()
		values[index].ID = id
		values[index].CreatedAt = createdAt
	}
	harness.metadata.list = func(_ context.Context, _ int, filter snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error) {
		if filter.Before == nil {
			return values, nil
		}
		return values[2:], nil
	}
	createdAfter := createdAt.Add(-time.Hour)
	query := url.Values{
		"type": {"opaque/v1"}, "content_state": {"available"},
		"created_after": {createdAfter.Format(time.RFC3339)}, "limit": {"2"},
	}
	request := httptest.NewRequest(http.MethodGet, "/snapshots?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("first page status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var first []snapshot.Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != 30 || first[1].ID != 29 {
		t.Fatalf("first page IDs = %+v, want [30 29]", first)
	}
	next := recorder.Header().Get("X-Next-Cursor")
	decoded, err := pagination.Decode(next)
	if err != nil {
		t.Fatalf("decode next cursor %q: %v", next, err)
	}
	if !decoded.CreatedAt.Equal(createdAt) || decoded.ID != 29 {
		t.Fatalf("next cursor = %#v, want (%s,29)", decoded, createdAt)
	}
	link := recorder.Header().Get("Link")
	start := strings.IndexByte(link, '<')
	end := strings.IndexByte(link, '>')
	if start != 0 || end < 0 || !strings.Contains(link, `rel="next"`) {
		t.Fatalf("malformed next Link = %q", link)
	}
	nextURL, err := url.Parse(link[start+1 : end])
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := query
	wantQuery.Set("cursor", next)
	if nextURL.EscapedPath() != "/snapshots" || nextURL.Query().Encode() != wantQuery.Encode() {
		t.Fatalf("next URL = %q, want path and filters %q", nextURL.String(), wantQuery.Encode())
	}

	recorder = httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, nextURL.String(), nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("second page status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var second []snapshot.Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != 28 {
		t.Fatalf("second page IDs = %+v, want [28]", second)
	}
	if recorder.Header().Get("X-Next-Cursor") != "" || recorder.Header().Get("Link") != "" {
		t.Fatalf("terminal page exposed next headers: %#v", recorder.Header())
	}
	if len(harness.metadata.listCalls) != 2 {
		t.Fatalf("list calls = %#v", harness.metadata.listCalls)
	}
	for index, call := range harness.metadata.listCalls {
		if call.filter.Limit != 3 {
			t.Fatalf("call %d fetch limit = %d, want 3", index, call.filter.Limit)
		}
	}
	before := harness.metadata.listCalls[1].filter.Before
	if before == nil || !before.CreatedAt.Equal(createdAt) || before.ID != 29 {
		t.Fatalf("second cursor = %#v", before)
	}
}

func TestListRejectsInvalidQueriesAndBodiesBeforeMetadata(t *testing.T) {
	tests := []string{
		"?type=opaque%2Fv1&type=review%2Fv1",
		"?type=Opaque",
		"?content_state=missing",
		"?created_after=yesterday",
		"?limit=0",
		"?limit=1001",
		"?limit=01",
		"?limit=%2B1",
		"?cursor=secret",
		"?unknown=value",
	}
	for _, suffix := range tests {
		t.Run(suffix, func(t *testing.T) {
			harness := newHandlerHarness(t)
			recorder := httptest.NewRecorder()
			harness.factory.List(harness.team).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/snapshots"+suffix, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if len(harness.metadata.listCalls) != 0 {
				t.Fatalf("metadata called: %#v", harness.metadata.listCalls)
			}
		})
	}

	harness := newHandlerHarness(t)
	recorder := httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/snapshots", strings.NewReader("body")))
	if recorder.Code != http.StatusBadRequest || len(harness.metadata.listCalls) != 0 {
		t.Fatalf("body status/calls = %d/%d", recorder.Code, len(harness.metadata.listCalls))
	}
}

func TestListMapsMetadataFailureToBoundedInternalError(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.list = func(context.Context, int, snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error) {
		return nil, errors.New("sql from /tmp/private")
	}
	recorder := httptest.NewRecorder()
	harness.factory.List(harness.team).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/snapshots", nil))
	response := decodeError(t, recorder)
	if recorder.Code != http.StatusInternalServerError || response.Error != "internal_error" || strings.Contains(recorder.Body.String(), "sql") {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestShowReturnsAuthorizedAvailableOrExpiredDetail(t *testing.T) {
	for _, state := range []snapshot.ContentState{snapshot.ContentStateAvailable, snapshot.ContentStateExpired} {
		t.Run(string(state), func(t *testing.T) {
			harness := newHandlerHarness(t)
			manifest := harness.manifest.Clone()
			manifest.ContentState = state
			detail := snapshot.Detail{
				Manifest:     manifest,
				ReplicaCount: 2,
				RetentionClaims: []snapshot.RetentionClaim{{
					ID: 9007199254740995, SnapshotID: manifest.ID, Class: snapshot.RetentionClassWorkflow,
					Reason: "run output", CreatedAt: manifest.CreatedAt,
				}},
				Productions: []snapshot.ProductionDetail{{
					ID: 9007199254740997, Kind: snapshot.ProductionKindBuild, CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: 42, PlanID: "plan", Attempt: "1", StepKind: "task", StepName: "produce",
					},
					OutputPort: "result", CreatedAt: manifest.CreatedAt, Inputs: []snapshot.ProductionInput{},
				}},
				Downstream: []snapshot.ProductionSummary{},
			}
			harness.metadata.detail = func(context.Context, int, snapshot.SnapshotID) (snapshot.Detail, bool, error) {
				return detail, true, nil
			}
			recorder := httptest.NewRecorder()
			harness.factory.Show(harness.team).ServeHTTP(recorder,
				snapshotRequest(http.MethodGet, "/snapshots/9007199254740993", "9007199254740993", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if len(harness.metadata.detailCalls) != 1 || harness.metadata.detailCalls[0] != (authorizedCall{teamID: 7, id: manifest.ID}) {
				t.Fatalf("detail calls = %#v", harness.metadata.detailCalls)
			}
			var got snapshot.Detail
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode detail: %v", err)
			}
			if got.Manifest.ID != manifest.ID || got.Manifest.ContentState != state || got.ReplicaCount != 2 {
				t.Fatalf("detail = %#v", got)
			}
			for _, exactID := range []string{
				`"id":"9007199254740995"`,
				`"id":"9007199254740997"`,
			} {
				if !bytes.Contains(recorder.Body.Bytes(), []byte(exactID)) {
					t.Fatalf("detail durable ID wire format is not quoted: %s", recorder.Body.String())
				}
			}
			for field, present := range map[string]bool{
				"retention_claims": bytes.Contains(recorder.Body.Bytes(), []byte(`"retention_claims":[`)),
				"productions":      bytes.Contains(recorder.Body.Bytes(), []byte(`"productions":[`)),
				"downstream":       bytes.Contains(recorder.Body.Bytes(), []byte(`"downstream":[]`)),
			} {
				if !present {
					t.Fatalf("%s was not encoded as an array: %s", field, recorder.Body.String())
				}
			}
		})
	}
}

func TestShowRejectsMalformedIDsQueriesAndBodiesBeforeMetadata(t *testing.T) {
	for _, id := range []string{"", "0", "01", "+1", "-1", " 1", "9223372036854775808"} {
		t.Run("id="+id, func(t *testing.T) {
			harness := newHandlerHarness(t)
			recorder := httptest.NewRecorder()
			harness.factory.Show(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/id", id, nil))
			if recorder.Code != http.StatusBadRequest || len(harness.metadata.detailCalls) != 0 {
				t.Fatalf("status/calls = %d/%d, body = %s", recorder.Code, len(harness.metadata.detailCalls), recorder.Body.String())
			}
		})
	}

	harness := newHandlerHarness(t)
	recorder := httptest.NewRecorder()
	request := snapshotRequest(http.MethodGet, "/snapshots/id?extra=value", "1", nil)
	harness.factory.Show(harness.team).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || len(harness.metadata.detailCalls) != 0 {
		t.Fatalf("query status/calls = %d/%d", recorder.Code, len(harness.metadata.detailCalls))
	}

	harness = newHandlerHarness(t)
	recorder = httptest.NewRecorder()
	harness.factory.Show(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/id", "1", strings.NewReader("body")))
	if recorder.Code != http.StatusBadRequest || len(harness.metadata.detailCalls) != 0 {
		t.Fatalf("body status/calls = %d/%d", recorder.Code, len(harness.metadata.detailCalls))
	}
}

func TestShowMakesMissingAndDeniedIndistinguishable(t *testing.T) {
	responses := make([]*httptest.ResponseRecorder, 0, 2)
	for _, result := range []struct {
		found bool
		err   error
	}{{found: false}, {err: snapshot.ErrNotFound}} {
		harness := newHandlerHarness(t)
		harness.metadata.detail = func(context.Context, int, snapshot.SnapshotID) (snapshot.Detail, bool, error) {
			return snapshot.Detail{}, result.found, result.err
		}
		recorder := httptest.NewRecorder()
		harness.factory.Show(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/1", "1", nil))
		if recorder.Code != http.StatusNotFound || decodeError(t, recorder).Error != "not_found" {
			t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
		responses = append(responses, recorder)
	}
	if responses[0].Body.String() != responses[1].Body.String() {
		t.Fatalf("missing and denied differ: %q vs %q", responses[0].Body.String(), responses[1].Body.String())
	}
}

func TestShowMapsUnexpectedMetadataFailureWithoutLeaking(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.detail = func(context.Context, int, snapshot.SnapshotID) (snapshot.Detail, bool, error) {
		return snapshot.Detail{}, false, errors.New("sql /tmp/private")
	}
	recorder := httptest.NewRecorder()
	harness.factory.Show(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/1", "1", nil))
	if recorder.Code != http.StatusInternalServerError || decodeError(t, recorder).Error != "internal_error" || strings.Contains(recorder.Body.String(), "/tmp") {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestContentAuthorizesThenStreamsWithExactCanonicalHeaders(t *testing.T) {
	harness := newHandlerHarness(t)
	reader := &trackedReadCloser{Reader: strings.NewReader("tar")}
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	harness.content.open = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return reader, nil
	}
	recorder := httptest.NewRecorder()
	harness.factory.Content(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodGet, "/snapshots/9007199254740993/content", "9007199254740993", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "tar" {
		t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
	}
	wantHeaders := map[string]string{
		"Content-Type":           "application/x-tar",
		"Content-Length":         "3",
		"ETag":                   `"` + harness.manifest.Digest.String() + `"`,
		"Content-Disposition":    `attachment; filename="snapshot-9007199254740993.tar"`,
		"Cache-Control":          "private, immutable",
		"X-Content-Type-Options": "nosniff",
	}
	for name, want := range wantHeaders {
		if got := recorder.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if len(harness.metadata.getCalls) != 1 || len(harness.content.openCalls) != 1 || reader.closed != 1 {
		t.Fatalf("get/open/closes = %d/%d/%d", len(harness.metadata.getCalls), len(harness.content.openCalls), reader.closed)
	}
	if harness.metadata.getCalls[0] != (authorizedCall{teamID: 7, id: harness.manifest.ID}) {
		t.Fatalf("authorized call = %#v", harness.metadata.getCalls[0])
	}
}

func TestContentDeniesBeforeOpeningStorageAndDistinguishesExpiry(t *testing.T) {
	tests := []struct {
		name   string
		result snapshot.Snapshot
		found  bool
		err    error
		status int
		code   string
	}{
		{name: "missing", found: false, status: 404, code: "not_found"},
		{name: "denied", err: snapshot.ErrNotFound, status: 404, code: "not_found"},
		{name: "expired", result: func() snapshot.Snapshot {
			manifest := newHandlerHarness(t).manifest
			manifest.ContentState = snapshot.ContentStateExpired
			return manifest
		}(), found: true, status: 410, code: "expired"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				return test.result, test.found, test.err
			}
			recorder := httptest.NewRecorder()
			id := "1"
			if test.result.ID.Validate() == nil {
				id = test.result.ID.String()
			}
			harness.factory.Content(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/id/content", id, nil))
			if recorder.Code != test.status || decodeError(t, recorder).Error != test.code {
				t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
			}
			if len(harness.content.openCalls) != 0 {
				t.Fatalf("storage opened before authorization: %#v", harness.content.openCalls)
			}
		})
	}
}

func TestContentMapsOpenFailureToUnavailableWithoutLeakingStorage(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	partial := &trackedReadCloser{Reader: strings.NewReader("partial")}
	harness.content.open = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return partial, errors.New("daemon node /tmp/private")
	}
	recorder := httptest.NewRecorder()
	harness.factory.Content(harness.team).ServeHTTP(recorder, snapshotRequest(http.MethodGet, "/snapshots/1/content", harness.manifest.ID.String(), nil))
	if recorder.Code != http.StatusServiceUnavailable || decodeError(t, recorder).Error != "content_unavailable" || strings.Contains(recorder.Body.String(), "daemon") {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	if partial.closed != 1 {
		t.Fatalf("partial reader closes = %d, want 1", partial.closed)
	}
}

func TestContentRejectsRangeAfterAuthorizationBeforeOpeningStorage(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	request := snapshotRequest(http.MethodGet, "/snapshots/1/content", harness.manifest.ID.String(), nil)
	request.Header.Set("Range", "bytes=0-1")
	recorder := httptest.NewRecorder()
	harness.factory.Content(harness.team).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestedRangeNotSatisfiable || len(harness.metadata.getCalls) != 1 || len(harness.content.openCalls) != 0 {
		t.Fatalf("status/get/open = %d/%d/%d", recorder.Code, len(harness.metadata.getCalls), len(harness.content.openCalls))
	}
}

func TestContentRejectsUnverifiedBytesBeforeCommittingResponse(t *testing.T) {
	tests := []struct {
		name         string
		manifestSize int64
		reader       *trackedReadCloser
		cancel       bool
	}{
		{name: "read error", manifestSize: 3, reader: &trackedReadCloser{Reader: &lateErrorReader{payload: []byte("ta"), err: errors.New("digest mismatch")}}},
		{name: "short body", manifestSize: 4, reader: &trackedReadCloser{Reader: strings.NewReader("tar")}},
		{name: "same length digest mismatch", manifestSize: 3, reader: &trackedReadCloser{Reader: strings.NewReader("bad")}},
		{name: "close error", manifestSize: 3, reader: &trackedReadCloser{Reader: strings.NewReader("tar"), closeErr: errors.New("verification close")}},
		{name: "cancellation", manifestSize: 3, reader: &trackedReadCloser{Reader: strings.NewReader("tar")}, cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			manifest := harness.manifest.Clone()
			manifest.ByteSize = test.manifestSize
			harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				return manifest, true, nil
			}
			harness.content.open = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
				return test.reader, nil
			}
			request := snapshotRequest(http.MethodGet, "/snapshots/1/content", manifest.ID.String(), nil)
			if test.cancel {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			recorder := httptest.NewRecorder()
			harness.factory.Content(harness.team).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable || decodeError(t, recorder).Error != "content_unavailable" ||
				test.reader.closed != 1 || len(harness.reports) != 1 || recorder.Header().Get("ETag") != "" {
				t.Fatalf("status/closes/reports = %d/%d/%v", recorder.Code, test.reader.closed, harness.reports)
			}
		})
	}
}

func TestPinUsesAuthenticatedActorAndExactDigestLease(t *testing.T) {
	harness := newHandlerHarness(t)
	lease := &fakeLease{digest: harness.manifest.Digest, covers: true}
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	harness.locks.acquire = func(_ context.Context, digests []snapshot.Digest) (snapshot.DigestLease, error) {
		if len(digests) != 1 || digests[0] != harness.manifest.Digest {
			t.Fatalf("leased digests = %v", digests)
		}
		return lease, nil
	}
	claim := snapshot.RetentionClaim{
		ID: 9007199254740999, SnapshotID: harness.manifest.ID, Class: snapshot.RetentionClassPin,
		Actor: "github:subject-1", Reason: "keep for review", CreatedAt: harness.manifest.CreatedAt,
	}
	harness.metadata.pin = func(_ context.Context, gotLease snapshot.DigestLease, teamID int, actor string, ref snapshot.SnapshotRef, reason string) (snapshot.RetentionClaim, error) {
		if gotLease != lease || !gotLease.Covers(ref.Digest) {
			t.Fatalf("pin did not receive covering lease")
		}
		return claim, nil
	}
	request := snapshotRequest(http.MethodPut, "/snapshots/id/pin", harness.manifest.ID.String(), strings.NewReader(`{"reason":"keep for review"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.factory.Pin(harness.team).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"id":"9007199254740999"`) {
		t.Fatalf("retention claim ID lost JSON precision: %s", recorder.Body.String())
	}
	if len(harness.metadata.pinCalls) != 1 {
		t.Fatalf("pin calls = %#v", harness.metadata.pinCalls)
	}
	call := harness.metadata.pinCalls[0]
	wantRef := snapshot.SnapshotRef{ID: harness.manifest.ID, Type: harness.manifest.Type, Digest: harness.manifest.Digest}
	if call.teamID != 7 || call.actor != "github:subject-1" || call.reason != "keep for review" || call.ref != wantRef {
		t.Fatalf("pin call = %#v", call)
	}
	if lease.closed != 1 {
		t.Fatalf("lease closes = %d", lease.closed)
	}
	var got snapshot.RetentionClaim
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got.ID != claim.ID || got.Actor != claim.Actor {
		t.Fatalf("claim = %#v, decode err = %v", got, err)
	}
}

func TestPinDefaultsMissingReason(t *testing.T) {
	harness := newHandlerHarness(t)
	lease := &fakeLease{digest: harness.manifest.Digest, covers: true}
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return harness.manifest, true, nil
	}
	harness.locks.acquire = func(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) { return lease, nil }
	harness.metadata.pin = func(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef, string) (snapshot.RetentionClaim, error) {
		return snapshot.RetentionClaim{
			ID: 1, SnapshotID: harness.manifest.ID, Class: snapshot.RetentionClassPin,
			Actor: "github:subject-1", Reason: "manual pin", CreatedAt: harness.manifest.CreatedAt,
		}, nil
	}
	request := snapshotRequest(http.MethodPut, "/snapshots/id/pin", harness.manifest.ID.String(), strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.factory.Pin(harness.team).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(harness.metadata.pinCalls) != 1 || harness.metadata.pinCalls[0].reason != "manual pin" {
		t.Fatalf("status/calls = %d/%#v", recorder.Code, harness.metadata.pinCalls)
	}
}

func TestPinRejectsMalformedMediaBodyAndQueryBeforeAuthorization(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		encoding    string
		path        string
		status      int
	}{
		{name: "missing media", body: `{}`, status: 415},
		{name: "media parameters", body: `{}`, contentType: "application/json; charset=utf-8", status: 415},
		{name: "encoding", body: `{}`, contentType: "application/json", encoding: "gzip", status: 415},
		{name: "malformed", body: `{`, contentType: "application/json", status: 400},
		{name: "not object", body: `null`, contentType: "application/json", status: 400},
		{name: "unknown field", body: `{"actor":"bob"}`, contentType: "application/json", status: 400},
		{name: "duplicate reason", body: `{"reason":"first","reason":"second"}`, contentType: "application/json", status: 400},
		{name: "trailing value", body: `{} {}`, contentType: "application/json", status: 400},
		{name: "null reason", body: `{"reason":null}`, contentType: "application/json", status: 400},
		{name: "blank reason", body: `{"reason":" "}`, contentType: "application/json", status: 400},
		{name: "control reason", body: `{"reason":"bad\u0000"}`, contentType: "application/json", status: 400},
		{name: "long reason", body: `{"reason":"` + strings.Repeat("x", 501) + `"}`, contentType: "application/json", status: 400},
		{name: "oversized body", body: strings.Repeat(" ", 5000), contentType: "application/json", status: 413},
		{name: "unknown query", body: `{}`, contentType: "application/json", path: "?actor=bob", status: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			request := snapshotRequest(http.MethodPut, "/snapshots/id/pin"+test.path, harness.manifest.ID.String(), strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			recorder := httptest.NewRecorder()
			harness.factory.Pin(harness.team).ServeHTTP(recorder, request)
			if recorder.Code != test.status || len(harness.metadata.getCalls) != 0 || len(harness.locks.calls) != 0 {
				t.Fatalf("status/get/locks = %d/%d/%d, body = %s", recorder.Code, len(harness.metadata.getCalls), len(harness.locks.calls), recorder.Body.String())
			}
		})
	}
}

func TestPinDeniesOrRejectsExpiredBeforeLease(t *testing.T) {
	tests := []struct {
		name     string
		manifest snapshot.Snapshot
		found    bool
		err      error
		status   int
	}{
		{name: "missing", found: false, status: 404},
		{name: "denied", err: snapshot.ErrNotFound, status: 404},
		{name: "expired", manifest: func() snapshot.Snapshot {
			manifest := newHandlerHarness(t).manifest
			manifest.ContentState = snapshot.ContentStateExpired
			return manifest
		}(), found: true, status: 409},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				return test.manifest, test.found, test.err
			}
			id := harness.manifest.ID.String()
			if test.manifest.ID.Validate() == nil {
				id = test.manifest.ID.String()
			}
			request := snapshotRequest(http.MethodPut, "/snapshots/id/pin", id, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			harness.factory.Pin(harness.team).ServeHTTP(recorder, request)
			if recorder.Code != test.status || len(harness.locks.calls) != 0 || len(harness.metadata.pinCalls) != 0 {
				t.Fatalf("status/locks/pins = %d/%d/%d", recorder.Code, len(harness.locks.calls), len(harness.metadata.pinCalls))
			}
		})
	}
}

func TestPinClosesPartialLeaseAndMapsConflicts(t *testing.T) {
	t.Run("partial lease", func(t *testing.T) {
		harness := newHandlerHarness(t)
		lease := &fakeLease{digest: harness.manifest.Digest, covers: true}
		harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return harness.manifest, true, nil
		}
		harness.locks.acquire = func(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) {
			return lease, errors.New("lock /tmp/private")
		}
		request := snapshotRequest(http.MethodPut, "/snapshots/id/pin", harness.manifest.ID.String(), strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		harness.factory.Pin(harness.team).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError || lease.closed != 1 || len(harness.metadata.pinCalls) != 0 || strings.Contains(recorder.Body.String(), "/tmp") {
			t.Fatalf("status/close/pins/body = %d/%d/%d/%s", recorder.Code, lease.closed, len(harness.metadata.pinCalls), recorder.Body.String())
		}
	})

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{{snapshot.ErrConflict, 409, "conflict"}, {snapshot.ErrExpired, 409, "conflict"}, {snapshot.ErrNotFound, 404, "not_found"}} {
		harness := newHandlerHarness(t)
		lease := &fakeLease{digest: harness.manifest.Digest, covers: true}
		harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return harness.manifest, true, nil
		}
		harness.locks.acquire = func(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) { return lease, nil }
		harness.metadata.pin = func(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef, string) (snapshot.RetentionClaim, error) {
			return snapshot.RetentionClaim{}, test.err
		}
		request := snapshotRequest(http.MethodPut, "/snapshots/id/pin", harness.manifest.ID.String(), strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		harness.factory.Pin(harness.team).ServeHTTP(recorder, request)
		if recorder.Code != test.status || decodeError(t, recorder).Error != test.code || lease.closed != 1 {
			t.Fatalf("status/body/close = %d/%s/%d", recorder.Code, recorder.Body.String(), lease.closed)
		}
	}
}

func TestUnpinUsesCurrentActorUnderLeaseForAvailableAndExpiredSnapshots(t *testing.T) {
	for _, state := range []snapshot.ContentState{snapshot.ContentStateAvailable, snapshot.ContentStateExpired} {
		t.Run(string(state), func(t *testing.T) {
			harness := newHandlerHarness(t)
			manifest := harness.manifest.Clone()
			manifest.ContentState = state
			lease := &fakeLease{digest: manifest.Digest, covers: true}
			harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				return manifest, true, nil
			}
			harness.locks.acquire = func(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) { return lease, nil }
			harness.metadata.unpin = func(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef) error { return nil }
			recorder := httptest.NewRecorder()
			harness.factory.Unpin(harness.team).ServeHTTP(recorder,
				snapshotRequest(http.MethodDelete, "/snapshots/id/pin", manifest.ID.String(), nil))
			if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 || len(harness.metadata.unpinCalls) != 1 || lease.closed != 1 {
				t.Fatalf("status/body/calls/close = %d/%q/%d/%d", recorder.Code, recorder.Body.String(), len(harness.metadata.unpinCalls), lease.closed)
			}
			call := harness.metadata.unpinCalls[0]
			if call.teamID != 7 || call.actor != "github:subject-1" || call.ref.ID != manifest.ID || call.ref.Type != manifest.Type || call.ref.Digest != manifest.Digest {
				t.Fatalf("unpin call = %#v", call)
			}
		})
	}
}

func TestUnpinRejectsBodyAndDeniesBeforeLease(t *testing.T) {
	harness := newHandlerHarness(t)
	recorder := httptest.NewRecorder()
	harness.factory.Unpin(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodDelete, "/snapshots/id/pin", harness.manifest.ID.String(), strings.NewReader("body")))
	if recorder.Code != http.StatusBadRequest || len(harness.metadata.getCalls) != 0 {
		t.Fatalf("body status/get = %d/%d", recorder.Code, len(harness.metadata.getCalls))
	}

	harness = newHandlerHarness(t)
	harness.metadata.get = func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return snapshot.Snapshot{}, false, nil
	}
	recorder = httptest.NewRecorder()
	harness.factory.Unpin(harness.team).ServeHTTP(recorder,
		snapshotRequest(http.MethodDelete, "/snapshots/id/pin", harness.manifest.ID.String(), nil))
	if recorder.Code != http.StatusNotFound || len(harness.locks.calls) != 0 {
		t.Fatalf("missing status/locks = %d/%d", recorder.Code, len(harness.locks.calls))
	}
}

func TestDisabledFactoryReturnsNotFoundForEveryHandler(t *testing.T) {
	t.Parallel()

	factory, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{Enabled: false})
	if err != nil {
		t.Fatalf("construct disabled factory: %v", err)
	}
	team := snapshotsapi.TrustedTeam{ID: 7, Name: "main"}
	handlers := map[string]http.Handler{
		"create":  factory.Create(team),
		"list":    factory.List(team),
		"show":    factory.Show(team),
		"content": factory.Content(team),
		"pin":     factory.Pin(team),
		"unpin":   factory.Unpin(team),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ignored", nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			if recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
			}
			var response snapshotsapi.ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error != "not_found" {
				t.Fatalf("disabled error = %#v, decode error = %v", response, err)
			}
		})
	}
}

func TestEnabledFactoryRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	_, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{
		Enabled:       true,
		ArchiveLimits: snapshot.ArchiveLimits{MaxContentBytes: 1, MaxEntries: 1},
	})
	if err == nil {
		t.Fatal("expected enabled construction to reject missing dependencies")
	}
}
