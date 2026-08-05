package snapshots

import (
	"bytes"
	"code.cloudfoundry.org/lager/v3"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/repodiff"
	"github.com/concourse/concourse/agent/snapshot"
)

const maxPinRequestBytes int64 = 4096

type HandlerFactory struct {
	logger            lager.Logger
	enabled           bool
	creator           SnapshotCreator
	metadata          MetadataStore
	content           ContentStore
	repositoryChanges RepositoryChangeProjectionReader
	locks             snapshot.DigestLockManager
	identity          RequestIdentityFunc
	reportError       ErrorReporter
	transportLimit    int64
	tempDir           string
}

func NewHandlerFactory(config Config) (*HandlerFactory, error) {
	factoryLogger := config.Logger
	if factoryLogger == nil {
		factoryLogger = lager.NewLogger("snapshots")
	}
	if !config.Enabled {
		return &HandlerFactory{enabled: false, logger: factoryLogger}, nil
	}
	for name, dependency := range map[string]any{
		"creator": config.Creator, "metadata store": config.Metadata,
		"content store": config.Content, "digest locks": config.Locks,
	} {
		if interfaceIsNil(dependency) {
			return nil, fmt.Errorf("snapshots API: %s is required when enabled", name)
		}
	}
	if config.Identity == nil {
		return nil, fmt.Errorf("snapshots API: request identity is required when enabled")
	}
	transportLimit, err := config.ArchiveLimits.CanonicalArchiveByteLimit()
	if err != nil {
		return nil, fmt.Errorf("snapshots API: archive limits: %w", err)
	}
	if strings.TrimSpace(config.TempDir) == "" || !filepath.IsAbs(config.TempDir) {
		return nil, fmt.Errorf("snapshots API: temp dir must be a dedicated absolute path")
	}
	if err := snapshot.ValidateTempDir(config.TempDir); err != nil {
		return nil, fmt.Errorf("snapshots API: temp dir: %w", err)
	}
	reporter := config.ReportError
	if reporter == nil {
		reporter = func(context.Context, string) {}
	}
	return &HandlerFactory{
		enabled: true, creator: config.Creator, metadata: config.Metadata,
		logger:  factoryLogger,
		content: config.Content, locks: config.Locks, identity: config.Identity,
		repositoryChanges: config.RepositoryChanges,
		reportError:       reporter, transportLimit: transportLimit,
		tempDir: config.TempDir,
	}, nil
}

func (factory *HandlerFactory) Create(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodPost, factory.create)
}
func (factory *HandlerFactory) List(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodGet, factory.list)
}
func (factory *HandlerFactory) Show(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodGet, factory.show)
}
func (factory *HandlerFactory) Content(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodGet, factory.contentHandler)
}
func (factory *HandlerFactory) Pin(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodPut, factory.pin)
}
func (factory *HandlerFactory) Unpin(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodDelete, factory.unpin)
}
func (factory *HandlerFactory) RepositoryChangeProjection(team TrustedTeam) http.Handler {
	return factory.endpoint(team, http.MethodGet, factory.repositoryChangeProjection)
}

type teamEndpoint func(http.ResponseWriter, *http.Request, TrustedTeam)

func (factory *HandlerFactory) endpoint(team TrustedTeam, method string, endpoint teamEndpoint) http.Handler {
	if factory == nil || !factory.enabled {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "snapshot service is not enabled")
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
			return
		}
		if err := validateAuthority(team.ID, team.Name); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
			return
		}
		endpoint(w, r, team)
	})
}

func (factory *HandlerFactory) create(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	typeRef, bases, ok := parseCreateQuery(w, r)
	if !ok {
		return
	}
	if !requireMediaType(w, r, "application/x-tar") {
		return
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "request content encoding is not supported")
		return
	}
	key, err := idempotencyKey(r.Header.Values("Idempotency-Key"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is invalid")
		return
	}
	identity, err := factory.identity(r)
	if err != nil || validateAuthorityString(identity.Actor, 500) != nil || validateAuthorityString(identity.DisplayName, 500) != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	body := &onceReadCloser{ReadCloser: r.Body}
	defer body.Close()
	if r.ContentLength > factory.transportLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "snapshot archive exceeds the configured limit")
		return
	}
	limited := &onceReadCloser{ReadCloser: http.MaxBytesReader(w, body, factory.transportLimit)}
	defer limited.Close()
	var openMu sync.Mutex
	opened := false
	openTar := func(ctx context.Context) (io.ReadCloser, error) {
		openMu.Lock()
		defer openMu.Unlock()
		if opened {
			return nil, fmt.Errorf("%w: upload request body was already opened", snapshot.ErrInvalidArchive)
		}
		opened = true
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return limited, nil
	}
	sourceMetadata, err := json.Marshal(struct {
		Adapter  string `json:"adapter"`
		Uploader string `json:"uploader"`
	}{Adapter: "upload", Uploader: identity.DisplayName})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	manifest, err := factory.creator.Upload(r.Context(), snapshot.UploadRequest{
		TeamID: team.ID, TeamName: team.Name, UploadedBy: identity.DisplayName,
		Actor: identity.Actor, IdempotencyKey: key, Type: typeRef,
		OpenTar: openTar, SourceMetadata: sourceMetadata,
		Bases: bases,
	})
	if err != nil {
		factory.writeSnapshotError(w, err)
		return
	}
	if err := manifest.Validate(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	writeJSON(w, http.StatusCreated, manifest)
}

func (factory *HandlerFactory) list(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	if !requireNoBody(w, r) {
		return
	}
	filter, ok := parseListQuery(w, r)
	if !ok {
		return
	}
	pageLimit := filter.Limit
	filter.Limit = pageLimit + 1
	values, err := factory.metadata.ListAuthorized(r.Context(), team.ID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if values == nil {
		values = []snapshot.Snapshot{}
	}
	if len(values) > filter.Limit {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	hasNext := len(values) > pageLimit
	if hasNext {
		values = values[:pageLimit]
	}
	for _, value := range values {
		if err := value.Validate(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
			return
		}
	}
	if hasNext {
		last := values[len(values)-1]
		cursor, err := pagination.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: int64(last.ID)})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
			return
		}
		setNextPageHeaders(w, r, cursor, pageLimit)
	}
	writeJSON(w, http.StatusOK, values)
}

func (factory *HandlerFactory) show(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	if !requireNoBody(w, r) {
		return
	}
	id, ok := parseSnapshotIDQuery(w, r)
	if !ok {
		return
	}
	detail, found, err := factory.metadata.GetAuthorizedDetail(r.Context(), team.ID, id)
	if errors.Is(err, snapshot.ErrNotFound) || !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if err := detail.Validate(); err != nil || detail.Manifest.ID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	detail = detail.Clone()
	if detail.RetentionClaims == nil {
		detail.RetentionClaims = []snapshot.RetentionClaim{}
	}
	if detail.Productions == nil {
		detail.Productions = []snapshot.ProductionDetail{}
	}
	if detail.Downstream == nil {
		detail.Downstream = []snapshot.ProductionSummary{}
	}
	for i := range detail.Productions {
		if detail.Productions[i].Inputs == nil {
			detail.Productions[i].Inputs = []snapshot.ProductionInput{}
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (factory *HandlerFactory) repositoryChangeProjection(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	if !requireNoBody(w, r) {
		return
	}
	id, ok := parseSnapshotIDQuery(w, r)
	if !ok {
		return
	}
	manifest, found, err := factory.metadata.GetAuthorized(r.Context(), team.ID, id)
	if errors.Is(err, snapshot.ErrNotFound) || !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if err := manifest.Validate(); err != nil || manifest.ID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if manifest.Type != "repository-change/v1" {
		writeError(w, http.StatusNotFound, "not_found", "repository-change projection was not found")
		return
	}
	if interfaceIsNil(factory.repositoryChanges) {
		writeError(w, http.StatusNotFound, "not_found", "repository-change projection service is not enabled")
		return
	}
	value, found, err := factory.repositoryChanges.GetRepositoryChangeProjection(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if !found {
		writeJSON(w, http.StatusAccepted, projection.RepositoryChange{
			SnapshotID: id, Status: projection.RepositoryChangeProjectionPending,
			Files: []repodiff.ChangedFile{},
		})
		return
	}
	if value.SnapshotID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	switch value.Status {
	case projection.RepositoryChangeProjectionPending,
		projection.RepositoryChangeProjectionUnavailable,
		projection.RepositoryChangeProjectionInvalid:
		value.Files = []repodiff.ChangedFile{}
		value.UnifiedDiff = ""
	case projection.RepositoryChangeProjectionReady:
		if value.FileCount != len(value.Files) || len(value.UnifiedDiff) > repodiff.BoundedUnifiedDiffBytes {
			writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
			return
		}
		if value.Files == nil {
			value.Files = []repodiff.ChangedFile{}
		}
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	// Persisted errors may contain object-store or private scratch details.
	// Status is public; raw operational diagnostics remain server-side.
	value.ProjectionError = ""
	writeJSON(w, http.StatusOK, value)
}

func (factory *HandlerFactory) contentHandler(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	if !requireNoBody(w, r) {
		return
	}
	id, ok := parseSnapshotIDQuery(w, r)
	if !ok {
		return
	}
	manifest, found, err := factory.metadata.GetAuthorized(r.Context(), team.ID, id)
	if errors.Is(err, snapshot.ErrNotFound) || !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
		return
	}
	if errors.Is(err, snapshot.ErrExpired) {
		writeError(w, http.StatusGone, "expired", "snapshot content has expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if err := manifest.Validate(); err != nil || manifest.ID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if manifest.ContentState == snapshot.ContentStateExpired {
		writeError(w, http.StatusGone, "expired", "snapshot content has expired")
		return
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if manifest.ByteSize > factory.transportLimit {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if len(r.Header.Values("Range")) != 0 {
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "range_not_supported", "snapshot content ranges are not supported")
		return
	}
	reader, err := factory.content.Open(r.Context(), manifest)
	if err != nil {
		if !interfaceIsNil(reader) {
			_ = reader.Close()
		}
		// Log the cause: "snapshot content is unavailable" is all the client
		// gets, and without this the reason existed nowhere. A 503 here is the
		// behavioural suite's agentic-workflow spec failing intermittently, and
		// the discarded err is the only thing that distinguishes "content is
		// genuinely still landing" from a real storage fault.
		factory.logger.Error("snapshot-content-open-failed", err, lager.Data{
			"digest": manifest.Digest, "snapshot": manifest.ID.String(),
		})
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "snapshot content is unavailable")
		return
	}
	if interfaceIsNil(reader) {
		factory.logger.Error("snapshot-content-open-returned-nil-reader", nil, lager.Data{
			"digest": manifest.Digest, "snapshot": manifest.ID.String(),
		})
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "snapshot content is unavailable")
		return
	}
	staged, err := factory.stageVerifiedContent(r.Context(), manifest, reader)
	if err != nil {
		factory.reportError(r.Context(), "snapshot_content_verification_failed")
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "snapshot content is unavailable")
		return
	}
	stagedName := staged.Name()
	defer func() {
		_ = staged.Close()
		_ = os.Remove(stagedName)
	}()

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.ByteSize, 10))
	w.Header().Set("ETag", `"`+manifest.Digest.String()+`"`)
	w.Header().Set("Content-Disposition", `attachment; filename="snapshot-`+manifest.ID.String()+`.tar"`)
	w.Header().Set("Cache-Control", "private, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	written, copyErr := io.Copy(w, requestContextReader{ctx: r.Context(), reader: staged})
	if copyErr != nil || written != manifest.ByteSize {
		factory.reportError(r.Context(), "snapshot_content_stream_failed")
		panic(http.ErrAbortHandler)
	}
}

func (factory *HandlerFactory) stageVerifiedContent(
	ctx context.Context,
	manifest snapshot.Snapshot,
	reader io.ReadCloser,
) (_ *os.File, err error) {
	staged, err := os.CreateTemp(factory.tempDir, "snapshot-download-*")
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	stagedName := staged.Name()
	keep := false
	defer func() {
		if keep {
			return
		}
		err = errors.Join(err, staged.Close(), os.Remove(stagedName))
	}()

	hasher := sha256.New()
	limited := io.LimitReader(requestContextReader{ctx: ctx, reader: reader}, manifest.ByteSize+1)
	written, copyErr := io.Copy(io.MultiWriter(staged, hasher), limited)
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return nil, errors.Join(copyErr, closeErr)
	}
	actualDigest := snapshot.Digest("sha256:" + hex.EncodeToString(hasher.Sum(nil)))
	if written != manifest.ByteSize || actualDigest != manifest.Digest {
		return nil, fmt.Errorf("snapshot content does not match its immutable manifest")
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	keep = true
	return staged, nil
}

func (factory *HandlerFactory) pin(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	id, ok := parseSnapshotIDQuery(w, r)
	if !ok {
		return
	}
	reason, ok := decodePinRequest(w, r)
	if !ok {
		return
	}
	identity, ok := factory.requestIdentity(w, r)
	if !ok {
		return
	}
	manifest, found, err := factory.metadata.GetAuthorized(r.Context(), team.ID, id)
	if errors.Is(err, snapshot.ErrNotFound) || !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if err := manifest.Validate(); err != nil || manifest.ID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if manifest.ContentState == snapshot.ContentStateExpired {
		writeError(w, http.StatusConflict, "conflict", "snapshot content must be recaptured before it can be pinned")
		return
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	var claim snapshot.RetentionClaim
	err = snapshot.WithDigestLease(r.Context(), factory.locks, []snapshot.Digest{manifest.Digest}, func(lease snapshot.DigestLease) error {
		if err := snapshot.RequireDigestLease(lease, manifest.Digest); err != nil {
			return err
		}
		var pinErr error
		claim, pinErr = factory.metadata.Pin(r.Context(), lease, team.ID, identity.Actor, ref, reason)
		return pinErr
	})
	if err != nil {
		factory.writeSnapshotError(w, err)
		return
	}
	if err := claim.Validate(); err != nil || claim.SnapshotID != manifest.ID || claim.Class != snapshot.RetentionClassPin || claim.Actor != identity.Actor {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (factory *HandlerFactory) unpin(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
	if !requireNoBody(w, r) {
		return
	}
	id, ok := parseSnapshotIDQuery(w, r)
	if !ok {
		return
	}
	identity, ok := factory.requestIdentity(w, r)
	if !ok {
		return
	}
	manifest, found, err := factory.metadata.GetAuthorized(r.Context(), team.ID, id)
	if errors.Is(err, snapshot.ErrNotFound) || !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	if err := manifest.Validate(); err != nil || manifest.ID != id {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	err = snapshot.WithDigestLease(r.Context(), factory.locks, []snapshot.Digest{manifest.Digest}, func(lease snapshot.DigestLease) error {
		if err := snapshot.RequireDigestLease(lease, manifest.Digest); err != nil {
			return err
		}
		return factory.metadata.Unpin(r.Context(), lease, team.ID, identity.Actor, ref)
	})
	if err != nil {
		factory.writeSnapshotError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (factory *HandlerFactory) requestIdentity(w http.ResponseWriter, r *http.Request) (RequestIdentity, bool) {
	identity, err := factory.identity(r)
	if err != nil || validateAuthorityString(identity.Actor, 500) != nil || validateAuthorityString(identity.DisplayName, 500) != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return RequestIdentity{}, false
	}
	return identity, true
}

func decodePinRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !requireMediaType(w, r, "application/json") {
		return "", false
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "request content encoding is not supported")
		return "", false
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	body := &onceReadCloser{ReadCloser: r.Body}
	defer body.Close()
	if r.ContentLength > maxPinRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "pin request exceeds the configured limit")
		return "", false
	}
	limited := http.MaxBytesReader(w, body, maxPinRequestBytes)
	raw, err := io.ReadAll(limited)
	closeErr := limited.Close()
	if err != nil || closeErr != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "pin request exceeds the configured limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "pin request body is invalid")
		}
		return "", false
	}
	if !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, "invalid_request", "pin request body is invalid")
		return "", false
	}
	value, err := decodePinWire(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "pin request body is invalid")
		return "", false
	}
	reason := "manual pin"
	if value != nil {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			writeError(w, http.StatusBadRequest, "invalid_reason", "pin reason is invalid")
			return "", false
		}
		if err := json.Unmarshal(value, &reason); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_reason", "pin reason is invalid")
			return "", false
		}
	}
	if validateAuthorityString(reason, 500) != nil {
		writeError(w, http.StatusBadRequest, "invalid_reason", "pin reason is invalid")
		return "", false
	}
	return reason, true
}

func decodePinWire(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("pin body must be an object")
	}
	var reason json.RawMessage
	seenReason := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok || key != "reason" || seenReason {
			return nil, fmt.Errorf("pin body contains an unsupported or duplicate field")
		}
		seenReason = true
		if err := decoder.Decode(&reason); err != nil {
			return nil, err
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("pin body is not terminated")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return reason, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func parseSnapshotIDQuery(w http.ResponseWriter, r *http.Request) (snapshot.SnapshotID, bool) {
	query, ok := strictQuery(w, r)
	if !ok {
		return 0, false
	}
	for key, values := range query {
		switch key {
		case ":snapshot_id":
			if len(values) != 1 || values[0] == "" {
				writeError(w, http.StatusBadRequest, "invalid_snapshot_id", "snapshot ID is invalid")
				return 0, false
			}
		case ":team_name":
			if len(values) != 1 {
				writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
				return 0, false
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return 0, false
		}
	}
	values, present := query[":snapshot_id"]
	if !present || len(values) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_snapshot_id", "snapshot ID is invalid")
		return 0, false
	}
	id, err := snapshot.ParseSnapshotID(values[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_snapshot_id", "snapshot ID is invalid")
		return 0, false
	}
	return id, true
}

func parseListQuery(w http.ResponseWriter, r *http.Request) (snapshot.SnapshotListFilter, bool) {
	filter := snapshot.SnapshotListFilter{Limit: 100}
	query, ok := strictQuery(w, r)
	if !ok {
		return snapshot.SnapshotListFilter{}, false
	}
	for key, values := range query {
		switch key {
		case "type", "content_state", "created_after", "limit", "cursor":
			if len(values) != 1 || values[0] == "" {
				writeError(w, http.StatusBadRequest, "invalid_query", "snapshot list query is invalid")
				return snapshot.SnapshotListFilter{}, false
			}
		case ":team_name":
			if len(values) != 1 {
				writeError(w, http.StatusBadRequest, "invalid_query", "snapshot list query is invalid")
				return snapshot.SnapshotListFilter{}, false
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_query", "snapshot list query contains unsupported fields")
			return snapshot.SnapshotListFilter{}, false
		}
	}
	if raw, present := query["type"]; present {
		value, err := snapshot.ParseTypeRef(raw[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_type", "snapshot type filter is invalid")
			return snapshot.SnapshotListFilter{}, false
		}
		filter.Type = value
	}
	if raw, present := query["content_state"]; present {
		value := snapshot.ContentState(raw[0])
		if err := value.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_content_state", "snapshot content state filter is invalid")
			return snapshot.SnapshotListFilter{}, false
		}
		filter.ContentState = value
	}
	if raw, present := query["created_after"]; present {
		value, err := time.Parse(time.RFC3339, raw[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_created_after", "created_after must be an RFC3339 timestamp")
			return snapshot.SnapshotListFilter{}, false
		}
		filter.CreatedAfter = &value
	}
	if raw, present := query["limit"]; present {
		value, err := parseCanonicalPositiveInt(raw[0], 1000)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a decimal integer from 1 to 1000")
			return snapshot.SnapshotListFilter{}, false
		}
		filter.Limit = value
	}
	if raw, present := query["cursor"]; present {
		value, err := pagination.Decode(raw[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "snapshot cursor is invalid")
			return snapshot.SnapshotListFilter{}, false
		}
		filter.Before = &value
	}
	return filter, true
}

func setNextPageHeaders(w http.ResponseWriter, r *http.Request, cursor string, limit int) {
	w.Header().Set("X-Next-Cursor", cursor)
	query := r.URL.Query()
	for key := range query {
		if strings.HasPrefix(key, ":") {
			query.Del(key)
		}
	}
	query.Set("cursor", cursor)
	query.Set("limit", strconv.Itoa(limit))
	w.Header().Set("Link", "<"+r.URL.EscapedPath()+"?"+query.Encode()+`>; rel="next"`)
}

func parseCanonicalPositiveInt(raw string, maximum int) (int, error) {
	if raw == "" || raw[0] == '0' {
		return 0, fmt.Errorf("not a canonical positive integer")
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("not a canonical positive integer")
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("outside allowed range")
	}
	return value, nil
}

func requireNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 ||
		r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody {
		writeError(w, http.StatusBadRequest, "unexpected_body", "request body is not allowed")
		return false
	}
	return true
}

func parseCreateQuery(w http.ResponseWriter, r *http.Request) (snapshot.TypeRef, map[string]snapshot.SnapshotID, bool) {
	query, ok := strictQuery(w, r)
	if !ok {
		return "", nil, false
	}
	for key, values := range query {
		switch key {
		case "type":
			if len(values) != 1 || values[0] == "" {
				writeError(w, http.StatusBadRequest, "invalid_query", "type must be provided exactly once")
				return "", nil, false
			}
		case "base":
			// Repeated and validated in detail below: the tar body carries the
			// archive, so a declared base input has nowhere to travel except
			// the query string.
		case ":team_name":
			if len(values) != 1 {
				writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
				return "", nil, false
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return "", nil, false
		}
	}
	values, present := query["type"]
	if !present || len(values) != 1 || values[0] == "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "type must be provided exactly once")
		return "", nil, false
	}
	typeRef, err := snapshot.ParseTypeRef(values[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_type", "snapshot type is invalid")
		return "", nil, false
	}
	bases, ok := parseDeclaredBaseQuery(w, query["base"])
	if !ok {
		return "", nil, false
	}
	return typeRef, bases, true
}

// parseDeclaredBaseQuery parses repeated base=NAME=SNAPSHOT-ID query values
// into a port-name -> snapshot-ID map. Every value must split into a
// non-empty name and a canonical positive decimal snapshot ID via the same
// snapshot.ParseSnapshotID the rest of this API uses, and no name may repeat
// — a caller cannot bind two different bases to the same declared input.
func parseDeclaredBaseQuery(w http.ResponseWriter, values []string) (map[string]snapshot.SnapshotID, bool) {
	if len(values) == 0 {
		return nil, true
	}
	bases := make(map[string]snapshot.SnapshotID, len(values))
	for _, raw := range values {
		name, idRaw, found := strings.Cut(raw, "=")
		if !found || name == "" || idRaw == "" {
			writeError(w, http.StatusBadRequest, "invalid_base", "base must be NAME=SNAPSHOT-ID")
			return nil, false
		}
		id, err := snapshot.ParseSnapshotID(idRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base", "base must be NAME=SNAPSHOT-ID")
			return nil, false
		}
		if _, duplicate := bases[name]; duplicate {
			writeError(w, http.StatusBadRequest, "invalid_base", "base declares the same name more than once")
			return nil, false
		}
		bases[name] = id
	}
	return bases, true
}

func strictQuery(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
		return nil, false
	}
	return query, true
}

func requireMediaType(w http.ResponseWriter, r *http.Request, required string) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request media type is not supported")
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, required) || len(parameters) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request media type is not supported")
		return false
	}
	return true
}

func idempotencyKey(values []string) (string, error) {
	if len(values) == 0 {
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", err
		}
		return "http-" + hex.EncodeToString(entropy[:]), nil
	}
	if len(values) != 1 || validateAuthorityString(values[0], 200) != nil {
		return "", fmt.Errorf("invalid idempotency key")
	}
	return values[0], nil
}

func validateAuthority(teamID int, teamName string) error {
	if teamID <= 0 {
		return fmt.Errorf("team ID must be positive")
	}
	return validateAuthorityString(teamName, 500)
}

func validateAuthorityString(value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("invalid authority string")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("invalid authority string")
		}
	}
	return nil
}

// writeSnapshotError maps a snapshot-service error onto a response, and logs
// the error it is mapping.
//
// Every branch here deliberately replaces the cause with a fixed message, so
// without this the wrapped detail died at the mapping. That is how the
// behavioural suite's intermittent 503 stayed unexplained: it reaches the
// client as "snapshot content is unavailable", which cannot distinguish
// content still landing from a genuine fault — and instrumenting the two
// content-serving sites missed it, because the failure is on the create path
// and arrives here as ErrContentUnavailable.
func (factory *HandlerFactory) writeSnapshotError(w http.ResponseWriter, err error) {
	factory.logger.Error("snapshot-request-failed", err)
	var maxBytesError *http.MaxBytesError
	var publicValidationFailure *snapshot.PublicValidationFailure
	switch {
	case errors.As(err, &maxBytesError), errors.Is(err, snapshot.ErrLimitExceeded):
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "snapshot archive exceeds the configured limit")
	case errors.Is(err, snapshot.ErrInvalidArchive):
		// This branch is deliberately ahead of the errors.As branch below, and
		// therefore has to read the public failure ITSELF.
		//
		// A canonicalizer rejection arrives here as ErrInvalidArchive joined with
		// a PublicValidationFailure. Falling through was never possible — the
		// switch stops at the first true case — so before this, every archive
		// rejection silently discarded its reason. Swapping the two branches
		// instead would answer 422 validation_failed, which is a different claim:
		// it says the bytes were understood and judged, when in fact they were
		// never parsed as an archive. The status stays 400 invalid_archive and the
		// classification rides along.
		if errors.As(err, &publicValidationFailure) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_archive",
				Message: publicValidationFailure.PublicMessage(),
				Reason:  string(publicValidationFailure.Reason()),
				Entry:   publicValidationFailure.Entry(),
			})
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_archive", "snapshot archive is invalid")
	case errors.Is(err, snapshot.ErrUnsupportedType):
		writeError(w, http.StatusBadRequest, "invalid_type", "snapshot type is unsupported")
	case errors.As(err, &publicValidationFailure):
		writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{
			Error:   "validation_failed",
			Message: publicValidationFailure.PublicMessage(),
			Reason:  string(publicValidationFailure.Reason()),
			Entry:   publicValidationFailure.Entry(),
		})
	case errors.Is(err, snapshot.ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "snapshot does not satisfy its declared type")
	case errors.Is(err, snapshot.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "snapshot request conflicts with immutable state")
	case errors.Is(err, snapshot.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
	case errors.Is(err, snapshot.ErrExpired):
		writeError(w, http.StatusConflict, "conflict", "snapshot content has expired")
	case errors.Is(err, snapshot.ErrContentUnavailable):
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "snapshot content is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		if status != http.StatusInternalServerError {
			writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type onceReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (reader *onceReadCloser) Close() error {
	reader.once.Do(func() { reader.err = reader.ReadCloser.Close() })
	return reader.err
}

type requestContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader requestContextReader) Read(destination []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(destination)
}
