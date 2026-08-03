package snapshots_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

// realSealer wires the handler's creator to the REAL BatchSealer over the REAL
// contract registry, so the tests below drive actual canonicalization and actual
// validation rather than a hand-written error.
//
// The metadata, content and lock stores are unstubbed fakes on purpose: every
// case here fails during capture or validation, which happens strictly before
// the sealer touches any of them. A case that started reaching a store would
// fail loudly on a zero-valued return rather than quietly passing.
func realSealer(t *testing.T) *snapshot.BatchSealer {
	t.Helper()
	registry, err := contracts.NewRegistry(
		contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}),
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	sealer, err := snapshot.NewBatchSealer(
		snapshot.Canonicalizer{TempDir: t.TempDir()},
		registry,
		&snapshotfakes.FakeMetadataStore{},
		&snapshotfakes.FakeContentStore{},
		&snapshotfakes.FakeDigestLockManager{},
	)
	if err != nil {
		t.Fatalf("NewBatchSealer(): %v", err)
	}
	return sealer
}

func postArchive(t *testing.T, harness *handlerHarness, typeRef string, raw []byte) (*httptest.ResponseRecorder, snapshotsapiErrorResponse) {
	t.Helper()
	harness.creator.upload = realSealer(t).Upload
	target := "/snapshots?type=" + url.QueryEscape(typeRef)
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/x-tar")
	recorder := httptest.NewRecorder()
	harness.factory.Create(harness.team).ServeHTTP(recorder, request)
	return recorder, decodeFailureResponse(t, recorder)
}

// snapshotsapiErrorResponse mirrors the wire envelope rather than reusing the Go
// type, so a field being renamed or dropped from the JSON is a test failure and
// not an invisible change to a published contract.
type snapshotsapiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Entry   string `json:"entry"`
}

func decodeFailureResponse(t *testing.T, recorder *httptest.ResponseRecorder) snapshotsapiErrorResponse {
	t.Helper()
	var response snapshotsapiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func tarBytes(t *testing.T, write func(*tar.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	write(writer)
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buffer.Bytes()
}

// rawTarArchive builds a single-entry ustar archive byte by byte. archive/tar's
// writer refuses to encode a trailing-slash name or a name containing control
// or invalid-UTF-8 bytes, which are exactly the archives a hostile or broken
// client sends and exactly the ones under test here.
func rawTarArchive(name string, typeflag byte, mode int64, linkname string, contents []byte) []byte {
	block := make([]byte, 512)
	copy(block[0:100], name)
	writeTarOctal(block[100:108], mode)
	writeTarOctal(block[108:116], 0)
	writeTarOctal(block[116:124], 0)
	writeTarOctal(block[124:136], int64(len(contents)))
	writeTarOctal(block[136:148], 0)
	for index := 148; index < 156; index++ {
		block[index] = ' '
	}
	block[156] = typeflag
	copy(block[157:257], linkname)
	copy(block[257:263], "ustar\x00")
	copy(block[263:265], "00")
	var checksum int64
	for _, value := range block {
		checksum += int64(value)
	}
	writeTarOctal(block[148:156], checksum)

	archive := append(block, contents...)
	if padding := (512 - len(contents)%512) % 512; padding != 0 {
		archive = append(archive, make([]byte, padding)...)
	}
	return append(archive, make([]byte, 1024)...)
}

func writeTarOctal(field []byte, value int64) {
	copy(field, fmt.Sprintf("%0*o", len(field)-1, value))
	field[len(field)-1] = 0
}

func tarFile(t *testing.T, name string, contents []byte) []byte {
	return tarBytes(t, func(writer *tar.Writer) {
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(contents)),
		}); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if _, err := writer.Write(contents); err != nil {
			t.Fatalf("write content %q: %v", name, err)
		}
	})
}

// TestArchiveRejectionsAnswerFourHundredWithTheirReasonAndEntry is the
// end-to-end proof for the archive half: a real tar, the real canonicalizer, the
// real HTTP mapping.
//
// The status assertion is not incidental. An archive rejection is a statement
// about the transport envelope and stays 400 invalid_archive; only the reason
// and the entry are new. Collapsing it into the 422 body-validation class would
// tell a client the bytes were understood and judged, which is exactly what did
// not happen.
func TestArchiveRejectionsAnswerFourHundredWithTheirReasonAndEntry(t *testing.T) {
	overLong := strings.Repeat("p", int(snapshot.MaxSnapshotPathBytes)+1)

	tests := []struct {
		name   string
		raw    func(*testing.T) []byte
		reason snapshot.ValidationFailureReason
		entry  string
	}{
		{
			name:   "a dot-slash entry",
			raw:    func(t *testing.T) []byte { return tarFile(t, "./record.json", []byte("{}")) },
			reason: snapshot.ArchivePathNotCanonical,
			entry:  "./record.json",
		},
		{
			name:   "a trailing separator",
			raw:    func(*testing.T) []byte { return rawTarArchive("dir/", tar.TypeReg, 0644, "", nil) },
			reason: snapshot.ArchivePathNotCanonical,
			entry:  "dir/",
		},
		{
			name: "a duplicate canonical path",
			raw: func(t *testing.T) []byte {
				return tarBytes(t, func(writer *tar.Writer) {
					for range 2 {
						if err := writer.WriteHeader(&tar.Header{
							Name: "dir/file", Typeflag: tar.TypeReg, Mode: 0644,
						}); err != nil {
							t.Fatalf("write header: %v", err)
						}
					}
				})
			},
			reason: snapshot.ArchivePathDuplicate,
			entry:  "dir/file",
		},
		{
			name: "an escaping symlink",
			raw: func(t *testing.T) []byte {
				return tarBytes(t, func(writer *tar.Writer) {
					if err := writer.WriteHeader(&tar.Header{
						Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/shadow",
					}); err != nil {
						t.Fatalf("write header: %v", err)
					}
				})
			},
			reason: snapshot.ArchiveSymlinkEscapesRoot,
			entry:  "link",
		},
		{
			name: "an entry path over the path bound",
			raw: func(t *testing.T) []byte {
				return tarFile(t, overLong, nil)
			},
			reason: snapshot.ArchivePathTooLong,
			entry:  overLong[:snapshot.MaxPublicEntryBytes-len(snapshot.PublicEntryTruncationMarker)] + snapshot.PublicEntryTruncationMarker,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			recorder, response := postArchive(t, harness, "opaque/v1", test.raw(t))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if response.Error != "invalid_archive" {
				t.Fatalf("error = %q, want invalid_archive", response.Error)
			}
			if response.Reason != string(test.reason) {
				t.Fatalf("reason = %q, want %q (body %s)", response.Reason, test.reason, recorder.Body.String())
			}
			if response.Entry != test.entry {
				t.Fatalf("entry = %q, want %q", response.Entry, test.entry)
			}
			if len(response.Entry) > snapshot.MaxPublicEntryBytes {
				t.Fatalf("entry is %d bytes, over the %d bound", len(response.Entry), snapshot.MaxPublicEntryBytes)
			}
		})
	}
}

// A caller-submitted entry name reaches the JSON body, so a name carrying
// terminal control bytes or invalid UTF-8 must be neutralized on the way out.
func TestArchiveEntryNamesAreSanitizedBeforeTheyReachTheBody(t *testing.T) {
	// A NUL cannot travel in a ustar name field — it terminates the field — so the
	// hostile bytes here are the ones that CAN arrive: an ANSI escape and invalid
	// UTF-8. The NUL case is covered where it can actually be constructed, against
	// the sanitizer itself in agent/snapshot. The ".." segment is what makes the
	// canonicalizer reject the entry in the first place.
	hostile := "\x1b[31mdir\xff\xfe/../file"
	harness := newHandlerHarness(t)
	recorder, response := postArchive(t, harness, "opaque/v1", rawTarArchive(hostile, tar.TypeReg, 0644, "", nil))

	if recorder.Code != http.StatusBadRequest || response.Error != "invalid_archive" {
		t.Fatalf("status/error = %d/%q: %s", recorder.Code, response.Error, recorder.Body.String())
	}
	if response.Reason != string(snapshot.ArchivePathNotCanonical) {
		t.Fatalf("reason = %q", response.Reason)
	}
	for _, forbidden := range []string{"\x1b", "\xff", "\xfe"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("raw byte %q survived into the body: %q", forbidden, recorder.Body.String())
		}
	}
	if response.Entry == hostile {
		t.Fatal("the hostile entry name reached the client verbatim")
	}
	if !strings.Contains(response.Entry, "dir") || !strings.Contains(response.Entry, "file") {
		t.Fatalf("sanitizing destroyed the part a caller needs: %q", response.Entry)
	}
}

// The body class gets the same end-to-end proof as the archive class: a real
// tar carrying a real contract document, the real registry validator, and the
// real HTTP mapping — answering 422 with the rule that was broken.
//
// The type here is a document contract rather than a record contract on
// purpose. Upload builds its validation context with NO declared inputs
// (snapshot.NewValidationContext(nil, nil)), and every record contract requires
// at least one subject bound to a declared input, so a review or diagnosis
// uploaded over HTTP is rejected at subject binding before its body is ever
// judged. Those bodies are reached through the SEAL path instead, which is
// where agent/snapshot/contracts asserts their reachability.
func TestContractRejectionsAnswerFourTwentyTwoWithTheirReason(t *testing.T) {
	tests := []struct {
		name     string
		typeRef  string
		fileName string
		document map[string]any
		reason   snapshot.ValidationFailureReason
	}{
		{
			name: "a work item captured_at that is not RFC 3339", typeRef: "work-item/v1",
			fileName: "work-item.json",
			document: map[string]any{
				"schema_version": "1.0.0", "adapter": "jira", "external_id": "PROJ-1",
				"revision": "3", "captured_at": "yesterday", "title": "Fix it", "body": "please",
			},
			reason: snapshot.RecordFieldTypeInvalid,
		},
		{
			name: "a work item missing a required field", typeRef: "work-item/v1",
			fileName: "work-item.json",
			document: map[string]any{
				"schema_version": "1.0.0", "adapter": "jira", "external_id": "PROJ-1",
				"revision": "3", "captured_at": "2026-07-22T12:00:00Z", "title": "  ", "body": "please",
			},
			reason: snapshot.RecordFieldMissing,
		},
		{
			name: "a schema_version outside the closed set", typeRef: "question/v1",
			fileName: "question.json",
			document: map[string]any{"schema_version": "2.0.0", "prompt": "which?", "context": "here"},
			reason:   snapshot.RecordFieldValueNotAllowed,
		},
		{
			name: "a document that is not there at all", typeRef: "question/v1",
			fileName: "unrelated.json", document: map[string]any{},
			reason: snapshot.RecordDocumentMissing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			encoded, err := json.Marshal(test.document)
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}
			recorder, response := postArchive(t, harness, test.typeRef, tarFile(t, test.fileName, encoded))

			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
			}
			if response.Error != "validation_failed" {
				t.Fatalf("error = %q, want validation_failed", response.Error)
			}
			if response.Reason != string(test.reason) {
				t.Fatalf("reason = %q, want %q (body %s)", response.Reason, test.reason, recorder.Body.String())
			}
			if response.Entry != "" {
				t.Fatalf("a body rejection carried an archive entry: %q", response.Entry)
			}
		})
	}
}

// TestArchiveAndValidationClassesKeepDistinctStatusesAndBranchOrder locks the
// trap the ordering in writeSnapshotError sets.
//
// errors.Is(err, ErrInvalidArchive) is tested BEFORE errors.As(&public). An
// archive rejection that also carries a public failure therefore has to be
// answered by the archive branch itself; if it fell through, or if the two
// branches were swapped, an archive rejection would silently change status.
// Both directions are asserted, so neither reordering can pass.
func TestArchiveAndValidationClassesKeepDistinctStatusesAndBranchOrder(t *testing.T) {
	cause := errors.New("private detail: /tmp/capture-771/root token=secret")

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
		wantReason string
		wantEntry  string
	}{
		{
			name: "an archive rejection carrying a public failure stays 400",
			err: errors.Join(
				snapshot.ErrInvalidArchive,
				snapshot.NewPublicValidationFailureForEntry(snapshot.ArchivePathDuplicate, "dir/file", cause),
			),
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_archive",
			wantReason: string(snapshot.ArchivePathDuplicate),
			wantEntry:  "dir/file",
		},
		{
			name: "an archive rejection with no public failure keeps the old envelope",
			err:  errors.Join(snapshot.ErrInvalidArchive, cause),

			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_archive",
			wantReason: "",
			wantEntry:  "",
		},
		{
			name: "a body rejection carrying a public failure stays 422",
			err: errors.Join(
				snapshot.ErrValidation,
				snapshot.NewPublicValidationFailure(snapshot.RecordBlockingInconsistent, cause),
			),
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  "validation_failed",
			wantReason: string(snapshot.RecordBlockingInconsistent),
			wantEntry:  "",
		},
		{
			name:       "a body rejection with no public failure keeps the old envelope",
			err:        errors.Join(snapshot.ErrValidation, cause),
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  "validation_failed",
			wantReason: "",
			wantEntry:  "",
		},
		{
			name: "a limit rejection still outranks both, public failure or not",
			err: errors.Join(
				snapshot.ErrLimitExceeded, snapshot.ErrInvalidArchive,
				snapshot.NewPublicValidationFailureForEntry(snapshot.ArchivePathTooLong, "dir/file", cause),
			),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  "limit_exceeded",
			wantReason: "",
			wantEntry:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			harness.creator.upload = func(_ context.Context, _ snapshot.UploadRequest) (snapshot.Snapshot, error) {
				return snapshot.Snapshot{}, test.err
			}
			request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", strings.NewReader("tar"))
			request.Header.Set("Content-Type", "application/x-tar")
			recorder := httptest.NewRecorder()
			harness.factory.Create(harness.team).ServeHTTP(recorder, request)
			response := decodeFailureResponse(t, recorder)

			if recorder.Code != test.wantStatus || response.Error != test.wantError {
				t.Fatalf("status/error = %d/%q, want %d/%q", recorder.Code, response.Error, test.wantStatus, test.wantError)
			}
			if response.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", response.Reason, test.wantReason)
			}
			if response.Entry != test.wantEntry {
				t.Fatalf("entry = %q, want %q", response.Entry, test.wantEntry)
			}
			for _, secret := range []string{"token=secret", "/tmp/capture-771"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("private cause leaked into the body: %s", recorder.Body.String())
				}
			}
		})
	}
}

// Every reason in the closed set has to survive the HTTP mapping. Reasons whose
// only producer is the seal-time path (a build output rather than an upload)
// still reach a client through this mapping, so the mapping is asserted for all
// of them here and reachability is asserted against the real validators in
// agent/snapshot and agent/snapshot/contracts.
func TestEveryPublicReasonSurvivesTheHTTPMapping(t *testing.T) {
	for _, reason := range everyPublicReasonForHTTP() {
		t.Run(string(reason), func(t *testing.T) {
			harness := newHandlerHarness(t)
			cause := fmt.Errorf("private cause for %s: token=secret", reason)
			harness.creator.upload = func(_ context.Context, _ snapshot.UploadRequest) (snapshot.Snapshot, error) {
				return snapshot.Snapshot{}, errors.Join(
					snapshot.ErrValidation,
					snapshot.NewPublicValidationFailure(reason, cause),
				)
			}
			request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", strings.NewReader("tar"))
			request.Header.Set("Content-Type", "application/x-tar")
			recorder := httptest.NewRecorder()
			harness.factory.Create(harness.team).ServeHTTP(recorder, request)
			response := decodeFailureResponse(t, recorder)

			if recorder.Code != http.StatusUnprocessableEntity || response.Error != "validation_failed" {
				t.Fatalf("status/error = %d/%q", recorder.Code, response.Error)
			}
			if response.Reason != string(reason) {
				t.Fatalf("reason = %q, want %q", response.Reason, reason)
			}
			if response.Message == "" {
				t.Fatal("a classified failure reached the client with no message")
			}
			if strings.Contains(recorder.Body.String(), "token=secret") {
				t.Fatalf("private cause leaked: %s", recorder.Body.String())
			}
		})
	}
}

func everyPublicReasonForHTTP() []snapshot.ValidationFailureReason {
	return []snapshot.ValidationFailureReason{
		snapshot.RepositoryMetadataMissing, snapshot.RepositoryMetadataUnsafe,
		snapshot.RepositoryHistoryIncomplete, snapshot.RepositoryObjectFormatUnsupported,
		snapshot.RepositoryGitlinksUnsupported, snapshot.RepositoryDirty, snapshot.RepositoryInvalid,

		snapshot.ArchivePathNotCanonical, snapshot.ArchivePathTooLong, snapshot.ArchivePathDuplicate,
		snapshot.ArchivePathParentInvalid, snapshot.ArchivePathCollides,
		snapshot.ArchiveEntryTypeUnsupported, snapshot.ArchiveEntryMetadataUnsupported,
		snapshot.ArchiveEntrySizeInvalid, snapshot.ArchiveSymlinkTargetInvalid,
		snapshot.ArchiveSymlinkEscapesRoot, snapshot.ArchiveStreamUnreadable,

		snapshot.RecordDocumentMissing, snapshot.RecordDocumentMalformed,
		snapshot.RecordEnvelopeInvalid, snapshot.RecordSubjectsInvalid,
		snapshot.RecordFieldMissing, snapshot.RecordFieldForbidden,
		snapshot.RecordFieldTypeInvalid, snapshot.RecordFieldValueNotAllowed,
		snapshot.RecordFieldOutOfRange, snapshot.RecordIdentifierInvalid,
		snapshot.RecordEntityIDDuplicate, snapshot.RecordEntityIDsUnsorted,
		snapshot.RecordAnchorInvalid, snapshot.RecordConclusionInconsistent,
		snapshot.RecordBlockingInconsistent, snapshot.RecordEvidenceMissing,
		snapshot.RecordRankInvalid, snapshot.RecordReferenceUnknown,
		snapshot.SnapshotTreeInvalid,
	}
}
