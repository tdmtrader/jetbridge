package fixtureagent_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func testAuthority(t *testing.T) fixtureagent.Authority {
	t.Helper()
	schema, found := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
	if !found {
		t.Fatal("review/v1 has no schema digest")
	}
	return fixtureagent.Authority{
		RecordType:    "review/v1",
		RecordSchema:  schema.String(),
		SubjectInput:  "change",
		SubjectType:   "opaque/v1",
		SubjectDigest: "sha256:" + strings.Repeat("d", 64),
	}
}

func TestEntriesSynthesizeAnAcceptedReviewFromInjectedAuthority(t *testing.T) {
	authority := testAuthority(t)
	entries, err := fixtureagent.Entries(fixtureagent.CaseReviewAccept, authority)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "record.json" {
		t.Fatalf("entries = %+v", entries)
	}

	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(entries[0].Body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.RecordVersion != contracts.RecordVersion {
		t.Fatalf("record_version = %q", record.RecordVersion)
	}
	if record.Type != snapshot.TypeRef("review/v1") {
		t.Fatalf("type = %q", record.Type)
	}
	if record.Schema.String() != authority.RecordSchema {
		t.Fatalf("schema = %q, want the injected %q", record.Schema, authority.RecordSchema)
	}
	if len(record.Subjects) != 1 {
		t.Fatalf("subjects = %+v", record.Subjects)
	}
	subject := record.Subjects[0]
	if subject.Role != contracts.SubjectRolePrimary || subject.Input != "change" {
		t.Fatalf("subject = %+v", subject)
	}
	if subject.Type != snapshot.TypeRef("opaque/v1") || subject.Digest.String() != authority.SubjectDigest {
		t.Fatalf("subject binding = %+v, want the injected input authority", subject)
	}
	if record.Body.Conclusion != "accept" || len(record.Body.Findings) != 0 {
		t.Fatalf("body = %+v", record.Body)
	}
}

func TestEntriesSynthesizeABlockingChangesRequiredReview(t *testing.T) {
	entries, err := fixtureagent.Entries(fixtureagent.CaseReviewChangesRequired, testAuthority(t))
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(entries[0].Body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Body.Conclusion != "changes-required" {
		t.Fatalf("conclusion = %q", record.Body.Conclusion)
	}
	if len(record.Body.Findings) != 1 || !record.Body.Findings[0].Blocking {
		t.Fatalf("findings = %+v", record.Body.Findings)
	}
	if record.Body.Findings[0].Evidence[0].Subject != "primary" {
		t.Fatalf("evidence = %+v", record.Body.Findings[0].Evidence)
	}
}

func TestTarEmitsTheSynthesizedTree(t *testing.T) {
	raw, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, testAuthority(t))
	if err != nil {
		t.Fatalf("Tar: %v", err)
	}
	names := tarNames(t, raw)
	if len(names) != 1 || names[0] != "record.json" {
		t.Fatalf("tar names = %v", names)
	}
}

func TestWriteTreeMaterializesTheSynthesizedTree(t *testing.T) {
	dir := t.TempDir()
	if err := fixtureagent.WriteTree(dir, fixtureagent.CaseReviewAccept, testAuthority(t)); err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Body.Conclusion != "accept" {
		t.Fatalf("conclusion = %q", record.Body.Conclusion)
	}
}

func TestEntriesRejectsAnUnknownCaseAndIncompleteAuthority(t *testing.T) {
	if _, err := fixtureagent.Entries("no-such-case", testAuthority(t)); err == nil || !strings.Contains(err.Error(), "unknown fixture case") {
		t.Fatalf("unknown case error = %v", err)
	}

	// Authority.validate() (fixtures.go:80-92) has five required-field checks;
	// witness all of them, not just RecordSchema, so a validation branch that
	// silently stops firing gets caught.
	for _, tc := range []struct {
		field   string
		mutate  func(*fixtureagent.Authority)
		wantErr string
	}{
		{"RecordType", func(a *fixtureagent.Authority) { a.RecordType = "" }, "record type"},
		{"RecordSchema", func(a *fixtureagent.Authority) { a.RecordSchema = "" }, "record schema"},
		{"SubjectInput", func(a *fixtureagent.Authority) { a.SubjectInput = "" }, "subject input"},
		{"SubjectType", func(a *fixtureagent.Authority) { a.SubjectType = "" }, "subject type"},
		{"SubjectDigest", func(a *fixtureagent.Authority) { a.SubjectDigest = "" }, "subject digest"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			incomplete := testAuthority(t)
			tc.mutate(&incomplete)
			if _, err := fixtureagent.Entries(fixtureagent.CaseReviewAccept, incomplete); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("incomplete authority (%s) error = %v", tc.field, err)
			}
		})
	}
}

func TestHostileCatalogViolatesExactlyOneRuleEach(t *testing.T) {
	authority := testAuthority(t)
	authority.OversizeBytes = 3072

	tests := []struct {
		name  string
		check func(*testing.T, []fixtureagent.Entry)
	}{
		{
			name: fixtureagent.CaseHostileTraversal,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				if entries[0].Path != "../escape" {
					t.Fatalf("first entry = %q, want a traversal path", entries[0].Path)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileSymlink,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				last := entries[len(entries)-1]
				if last.LinkTarget != "../../etc/passwd" {
					t.Fatalf("last entry = %+v, want an escaping symlink", last)
				}
				// The symlink is appended after a normal record.json (see
				// fixtures.go's CaseHostileSymlink); confirm that record is
				// still present and undamaged, so the escaping symlink really
				// is the only violation this case introduces.
				record := decodeReview(t, entries)
				if record.Schema.String() != authority.RecordSchema {
					t.Fatalf("schema = %q, want the intact injected authority", record.Schema)
				}
				if record.Subjects[0].Input != "change" {
					t.Fatalf("subject input = %q, want the intact injected authority", record.Subjects[0].Input)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileUnexposedSubject,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if record.Subjects[0].Input != "not-a-declared-input" {
					t.Fatalf("subject input = %q", record.Subjects[0].Input)
				}
				// record.json is present (decodeReview would already have
				// failed otherwise); confirm it is otherwise intact too, so
				// this case violates only the subject-input binding and
				// nothing else.
				if record.Schema.String() != authority.RecordSchema {
					t.Fatalf("schema = %q, want the intact injected authority", record.Schema)
				}
				if record.Subjects[0].Type.String() != authority.SubjectType {
					t.Fatalf("subject type = %q, want the intact injected authority", record.Subjects[0].Type)
				}
				if record.Subjects[0].Digest.String() != authority.SubjectDigest {
					t.Fatalf("subject digest = %q, want the intact injected authority", record.Subjects[0].Digest)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileSchemaDigest,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if record.Schema.String() == authority.RecordSchema {
					t.Fatal("schema digest was not corrupted")
				}
				if err := record.Schema.Validate(); err != nil {
					t.Fatalf("corrupted schema must still be a well-formed digest: %v", err)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileDuplicateFinding,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if len(record.Body.Findings) != 2 || record.Body.Findings[0].ID != record.Body.Findings[1].ID {
					t.Fatalf("findings = %+v, want two with the same id", record.Body.Findings)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileMissingRecord,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				for _, entry := range entries {
					if entry.Path == "record.json" {
						t.Fatal("record.json must be absent")
					}
				}
				if len(entries) == 0 {
					t.Fatal("the tree must not be empty, or capture fails for another reason")
				}
			},
		},
		{
			name: fixtureagent.CaseHostileOversized,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				// 3072 is deliberately NOT fixtureagent's own default
				// (4096, see defaultOversizeBytes): a fixture that ignored
				// the injected Authority.OversizeBytes field entirely and
				// fell back to the default would still produce a payload,
				// just the wrong number of bytes, and this exact-length
				// assertion is what catches that.
				for _, entry := range entries {
					if entry.Path == "payload.bin" {
						if len(entry.Body) != 3072 {
							t.Fatalf("payload = %d bytes, want 3072", len(entry.Body))
						}
						return
					}
				}
				t.Fatal("no payload.bin entry")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := fixtureagent.Entries(tc.name, authority)
			if err != nil {
				t.Fatalf("Entries(%q): %v", tc.name, err)
			}
			tc.check(t, entries)
			if _, err := fixtureagent.Tar(tc.name, authority); err != nil {
				t.Fatalf("Tar(%q): %v", tc.name, err)
			}
		})
	}
}

func TestCasesEnumeratesEveryCaseAndWriteTreeRefusesTheTarOnlyOne(t *testing.T) {
	authority := testAuthority(t)
	cases := fixtureagent.Cases()
	if len(cases) != 9 {
		t.Fatalf("Cases() = %v, want all nine", cases)
	}
	for _, name := range cases {
		if _, err := fixtureagent.Entries(name, authority); err != nil {
			t.Fatalf("Entries(%q): %v", name, err)
		}
	}
	// A `../` path cannot be materialized inside a destination directory: that
	// is a tar-layer attack only, and pretending otherwise would silently write
	// outside the output mount.
	if err := fixtureagent.WriteTree(t.TempDir(), fixtureagent.CaseHostileTraversal, authority); err == nil ||
		!strings.Contains(err.Error(), "cannot be materialized on disk") {
		t.Fatalf("WriteTree(traversal) error = %v", err)
	}
}

func decodeReview(t *testing.T, entries []fixtureagent.Entry) contracts.Record[contracts.ReviewBody] {
	t.Helper()
	for _, entry := range entries {
		if entry.Path != "record.json" {
			continue
		}
		var record contracts.Record[contracts.ReviewBody]
		if err := json.Unmarshal(entry.Body, &record); err != nil {
			t.Fatalf("decode record.json: %v", err)
		}
		return record
	}
	t.Fatal("no record.json entry")
	return contracts.Record[contracts.ReviewBody]{}
}

func tarNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var names []string
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, header.Name)
	}
}
