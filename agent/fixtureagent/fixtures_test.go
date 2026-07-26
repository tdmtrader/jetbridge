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
	incomplete := testAuthority(t)
	incomplete.RecordSchema = ""
	if _, err := fixtureagent.Entries(fixtureagent.CaseReviewAccept, incomplete); err == nil || !strings.Contains(err.Error(), "record schema") {
		t.Fatalf("incomplete authority error = %v", err)
	}
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
