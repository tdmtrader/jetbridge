// Package fixtureagent synthesizes the output trees a deterministic stand-in
// agent writes at an agent step's seal boundary.
//
// It is the single description of "what a conforming agent produced", shared by
// three consumers so they cannot drift: cmd/fixture-agent materializes these
// trees into AGENT_OUTPUT_<NAME> destinations inside a real pod, the atc/exec
// specs hand Tar() straight to a fake output volume so AgentStep meets the real
// snapshot.BatchSealer, and the behavioral suite runs the binary's image.
//
// Record envelopes are built from the PLATFORM-INJECTED authority, never from
// compiled-in constants. The runner prompt tells every agent that the type,
// schema, and input digest values it was handed "are verified again when the
// output is sealed" (agent/runner/runner.go); a fixture that pinned its own
// constants would be testing a promise nobody makes.
package fixtureagent

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The benign cases.
const (
	CaseReviewAccept          = "review-accept"
	CaseReviewChangesRequired = "review-changes-required"
)

// The hostile catalog. Each case violates exactly one rule, so the error it
// produces at the seal boundary names that rule and nothing else.
const (
	// CaseHostileTraversal puts a `../` segment in a tar entry name. It is
	// TAR-ONLY: WriteTree refuses it rather than escaping the output mount.
	CaseHostileTraversal = "hostile-traversal"
	// CaseHostileSymlink adds a symlink whose target resolves above the root.
	CaseHostileSymlink = "hostile-symlink"
	// CaseHostileUnexposedSubject anchors the record's primary subject at an
	// input the step never declared.
	CaseHostileUnexposedSubject = "hostile-unexposed-subject"
	// CaseHostileSchemaDigest pins a well-formed digest that is not the
	// platform-injected one.
	CaseHostileSchemaDigest = "hostile-schema-digest"
	// CaseHostileDuplicateFinding repeats an entity-set id.
	CaseHostileDuplicateFinding = "hostile-duplicate-finding"
	// CaseHostileMissingRecord writes a non-empty tree with no record.json.
	CaseHostileMissingRecord = "hostile-missing-record"
	// CaseHostileOversized writes a payload larger than the ARCHIVE-layer
	// configured limit (snapshot.Canonicalizer.MaxContentBytes). It is not
	// about the contracts-layer document limits.
	CaseHostileOversized = "hostile-oversized"
)

// defaultOversizeBytes is comfortably above the small MaxContentBytes the exec
// specs inject and comfortably below anything a real deployment configures.
const defaultOversizeBytes = 4096

// Authority is the server-owned truth an agent step injects into its pod: the
// declared type and current schema digest of one record output, and the type
// and digest of the input that output's primary subject binds to.
type Authority struct {
	RecordType    string
	RecordSchema  string
	SubjectInput  string
	SubjectType   string
	SubjectDigest string

	// OversizeBytes sizes CaseHostileOversized's payload. Zero means the
	// package default.
	OversizeBytes int
}

func (a Authority) validate() error {
	if strings.TrimSpace(a.RecordType) == "" {
		return fmt.Errorf("fixtureagent: record type is required")
	}
	if strings.TrimSpace(a.RecordSchema) == "" {
		return fmt.Errorf("fixtureagent: record schema is required")
	}
	if strings.TrimSpace(a.SubjectInput) == "" {
		return fmt.Errorf("fixtureagent: subject input is required")
	}
	if strings.TrimSpace(a.SubjectType) == "" {
		return fmt.Errorf("fixtureagent: subject type is required")
	}
	if strings.TrimSpace(a.SubjectDigest) == "" {
		return fmt.Errorf("fixtureagent: subject digest is required")
	}
	return nil
}

// Entry is one node of a fixture tree. A non-empty LinkTarget makes it a
// symlink and Body is ignored.
type Entry struct {
	Path       string
	Body       []byte
	LinkTarget string
}

// Cases lists every fixture case, sorted, for help text and table tests.
func Cases() []string {
	return []string{
		CaseHostileDuplicateFinding,
		CaseHostileMissingRecord,
		CaseHostileOversized,
		CaseHostileSchemaDigest,
		CaseHostileSymlink,
		CaseHostileTraversal,
		CaseHostileUnexposedSubject,
		CaseReviewAccept,
		CaseReviewChangesRequired,
	}
}

// Entries returns the tree for one case. Entries are emitted in the order they
// must be written; Tar preserves that order so a hostile entry is reached in a
// predictable position.
func Entries(caseName string, authority Authority) ([]Entry, error) {
	if err := authority.validate(); err != nil {
		return nil, err
	}
	switch caseName {
	case CaseReviewAccept:
		return recordEntries(reviewRecord(authority, acceptBody()))
	case CaseReviewChangesRequired:
		return recordEntries(reviewRecord(authority, changesRequiredBody()))
	case CaseHostileTraversal:
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append([]Entry{{Path: "../escape", Body: []byte("escaped\n")}}, entries...), nil
	case CaseHostileSymlink:
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append(entries, Entry{Path: "escape", LinkTarget: "../../etc/passwd"}), nil
	case CaseHostileUnexposedSubject:
		hostile := authority
		hostile.SubjectInput = "not-a-declared-input"
		return recordEntries(reviewRecord(hostile, acceptBody()))
	case CaseHostileSchemaDigest:
		hostile := authority
		hostile.RecordSchema = "sha256:" + strings.Repeat("f", 64)
		return recordEntries(reviewRecord(hostile, acceptBody()))
	case CaseHostileDuplicateFinding:
		body := changesRequiredBody()
		body.Findings = append(body.Findings, blockingFinding("F-1"))
		return recordEntries(reviewRecord(authority, body))
	case CaseHostileMissingRecord:
		return []Entry{{Path: "notes.txt", Body: []byte("the fixture agent forgot record.json\n")}}, nil
	case CaseHostileOversized:
		size := authority.OversizeBytes
		if size <= 0 {
			size = defaultOversizeBytes
		}
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append(entries, Entry{Path: "payload.bin", Body: bytes.Repeat([]byte("A"), size)}), nil
	default:
		return nil, fmt.Errorf("fixtureagent: unknown fixture case %q", caseName)
	}
}

// Tar renders a case as a raw uncompressed tar stream — the exact shape
// AgentStep hands the sealer through Artifact.StreamOut.
func Tar(caseName string, authority Authority) ([]byte, error) {
	entries, err := Entries(caseName, authority)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Path, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(entry.Body))}
		if entry.LinkTarget != "" {
			header = &tar.Header{Name: entry.Path, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: entry.LinkTarget}
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("fixtureagent: write header %q: %w", entry.Path, err)
		}
		if entry.LinkTarget == "" {
			if _, err := writer.Write(entry.Body); err != nil {
				return nil, fmt.Errorf("fixtureagent: write body %q: %w", entry.Path, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("fixtureagent: close tar: %w", err)
	}
	return buffer.Bytes(), nil
}

// WriteTree materializes a case into an existing directory. It is the pod-side
// face: a real agent writes files, not tars.
func WriteTree(dir, caseName string, authority Authority) error {
	entries, err := Entries(caseName, authority)
	if err != nil {
		return err
	}
	if caseName == CaseHostileTraversal {
		return fmt.Errorf("fixtureagent: case %q cannot be materialized on disk; it is a tar-layer attack", caseName)
	}
	for _, entry := range entries {
		full := filepath.Join(dir, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("fixtureagent: create parent of %q: %w", entry.Path, err)
		}
		if entry.LinkTarget != "" {
			if err := os.Symlink(filepath.FromSlash(entry.LinkTarget), full); err != nil {
				return fmt.Errorf("fixtureagent: symlink %q: %w", entry.Path, err)
			}
			continue
		}
		if err := os.WriteFile(full, entry.Body, 0o644); err != nil {
			return fmt.Errorf("fixtureagent: write %q: %w", entry.Path, err)
		}
	}
	return nil
}

func recordEntries(record contracts.Record[contracts.ReviewBody]) ([]Entry, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("fixtureagent: marshal record.json: %w", err)
	}
	return []Entry{{Path: "record.json", Body: body}}, nil
}

// reviewRecord assembles the envelope by hand rather than through
// contracts.NewRecord: NewRecord stamps the CURRENT compiled-in schema digest,
// and the whole point of the fixture is to copy the value the platform handed
// it — including, for CaseHostileSchemaDigest, a value that is deliberately not
// the current one.
func reviewRecord(authority Authority, body contracts.ReviewBody) contracts.Record[contracts.ReviewBody] {
	return contracts.Record[contracts.ReviewBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          snapshot.TypeRef(authority.RecordType),
		Schema:        snapshot.Digest(authority.RecordSchema),
		Subjects: []contracts.Subject{{
			ID:     "primary",
			Role:   contracts.SubjectRolePrimary,
			Input:  authority.SubjectInput,
			Type:   snapshot.TypeRef(authority.SubjectType),
			Digest: snapshot.Digest(authority.SubjectDigest),
		}},
		Body: body,
	}
}

func acceptBody() contracts.ReviewBody {
	return contracts.ReviewBody{
		Conclusion: "accept",
		Summary:    "fixture agent reviewed the exposed subject",
		Findings:   []contracts.Finding{},
	}
}

func changesRequiredBody() contracts.ReviewBody {
	return contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "fixture agent found one blocking defect",
		Findings:   []contracts.Finding{blockingFinding("F-1")},
	}
}

func blockingFinding(id string) contracts.Finding {
	start, end := 1, 1
	return contracts.Finding{
		ID: id, Severity: "high", Blocking: true,
		Category: "correctness", Title: "fixture blocking finding",
		Description: "the fixture agent always reports this finding",
		Evidence: []contracts.Anchor{{
			Subject: "primary",
			Locator: contracts.Locator{Kind: "file-lines", Path: "payload.txt", Start: &start, End: &end},
		}},
		Recommendation: "no action; this is a fixture",
	}
}
