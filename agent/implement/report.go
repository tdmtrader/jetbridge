package implement

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/capture"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed implement.schema.json
var summarySchema string

//go:embed implement-profile.md
var defaultProfile string

// Result file names inside a published change.
const (
	PatchFile   = "change.patch"
	SummaryFile = "summary.json"
)

const (
	notExecuted  = "Tests and repository code were not executed."
	noChanges    = "No files were changed."
	editOnly     = "edit-only"
	providerName = "codex"
)

func Schema() []byte         { return []byte(summarySchema) }
func DefaultProfile() []byte { return []byte(defaultProfile) }

// AssessmentSchema derives the model contract from the summary's schema. The
// changed files, patch digest, provenance and Run identity are supplied only
// by the worker.
func AssessmentSchema() []byte {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		panic(err)
	}
	defs := root["$defs"].(map[string]any)
	assessment := defs["assessment"].(map[string]any)
	assessment["$schema"] = root["$schema"]
	b, err := json.Marshal(assessment)
	if err != nil {
		panic(err)
	}
	return b
}

var (
	compiledSummary    = jsonschema.MustCompileString("summary.json", summarySchema)
	compiledAssessment = jsonschema.MustCompileString("assessment.json", string(AssessmentSchema()))
)

type Assessment struct {
	Summary     string   `json:"summary"`
	Complete    bool     `json:"complete"`
	Limitations []string `json:"limitations"`
}

type Provenance struct {
	InputDigest     string `json:"input_digest"`
	BaseCommit      string `json:"base_commit"`
	ProfileDigest   string `json:"profile_digest"`
	Provider        string `json:"provider"`
	CodexVersion    string `json:"codex_version"`
	ModelRequested  string `json:"model_requested"`
	ExecutionPolicy string `json:"execution_policy"`
}

// Summary is summary.json, implement/v1.
type Summary struct {
	SchemaVersion string        `json:"schema_version"`
	RunID         *int          `json:"run_id"`
	Provenance    Provenance    `json:"provenance"`
	Summary       string        `json:"summary"`
	Complete      bool          `json:"complete"`
	ChangedFiles  []ChangedFile `json:"changed_files"`
	Limitations   []string      `json:"limitations"`
	PatchDigest   string        `json:"patch_digest"`
}

type Metadata struct {
	RunID          *int
	ModelRequested string
	CodexVersion   string
	Profile        []byte
}

func decodeSchema(data []byte, schema *jsonschema.Schema, value any) error {
	if len(data) > capture.MaxFileBytes || !utf8.Valid(data) {
		return errors.New("invalid or oversized implementation JSON")
	}
	var raw any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&raw); err != nil {
		return errors.New("invalid implementation JSON")
	}
	if err := schema.Validate(raw); err != nil {
		return errors.New("implementation summary does not conform to the JSON schema")
	}
	return capture.DecodeStrict(data, value)
}

// BuildSummary validates the model's assessment and stamps everything the
// model does not get to say: provenance, Run identity, the changed files and
// the patch digest. The patch must already be verified against the snapshot.
func BuildSummary(s *Snapshot, patch []byte, changes []ChangedFile, assessmentJSON []byte, meta Metadata) (*Summary, error) {
	var a Assessment
	if err := decodeSchema(assessmentJSON, compiledAssessment, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Summary) == "" {
		return nil, errors.New("empty summary")
	}
	if changes == nil {
		changes = []ChangedFile{}
	}
	if len(changes) == 0 {
		a.Complete = false
		a.Limitations = append(a.Limitations, noChanges)
	}
	a.Limitations = append(a.Limitations, notExecuted)
	summary := &Summary{
		SchemaVersion: "implement/v1", RunID: meta.RunID,
		Provenance: Provenance{
			InputDigest: s.Digest, BaseCommit: s.Manifest.BaseCommit, ProfileDigest: capture.Digest(meta.Profile),
			Provider: providerName, CodexVersion: meta.CodexVersion, ModelRequested: meta.ModelRequested, ExecutionPolicy: editOnly,
		},
		Summary: a.Summary, Complete: a.Complete, ChangedFiles: changes, Limitations: a.Limitations,
		PatchDigest: capture.Digest(patch),
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	return ParseChange(data, patch, s)
}

// ParsePublishedChange checks a published change without its snapshot: the
// summary's schema, the patch digest, the patch's canonical form, and that
// changed_files is exactly what the patch touches.
func ParsePublishedChange(summaryJSON, patch []byte) (*Summary, error) {
	var s Summary
	if err := decodeSchema(summaryJSON, compiledSummary, &s); err != nil {
		return nil, err
	}
	if capture.Digest(patch) != s.PatchDigest {
		return nil, errors.New("change.patch does not match the summary's patch digest")
	}
	sections, err := ParsePatch(patch)
	if err != nil {
		return nil, fmt.Errorf("change.patch: %w", err)
	}
	got := changedFiles(sections)
	if len(got) != len(s.ChangedFiles) {
		return nil, errors.New("changed_files does not match the patch")
	}
	for i := range got {
		if got[i] != s.ChangedFiles[i] {
			return nil, errors.New("changed_files does not match the patch")
		}
	}
	if !strings.Contains(strings.Join(s.Limitations, "\n"), notExecuted) {
		return nil, errors.New("summary lacks the non-execution disclosure")
	}
	if len(got) == 0 && s.Complete {
		return nil, errors.New("an empty change cannot be complete")
	}
	return &s, nil
}

// ParseChange additionally binds a change to its snapshot: provenance must
// name it, and the patch must apply to its base exactly.
func ParseChange(summaryJSON, patch []byte, snapshot *Snapshot) (*Summary, error) {
	s, err := ParsePublishedChange(summaryJSON, patch)
	if err != nil {
		return nil, err
	}
	if s.Provenance.InputDigest != snapshot.Digest || s.Provenance.BaseCommit != snapshot.Manifest.BaseCommit {
		return nil, errors.New("summary provenance does not match the snapshot")
	}
	base, err := snapshot.tree()
	if err != nil {
		return nil, err
	}
	sections, err := ParsePatch(patch)
	if err != nil {
		return nil, err
	}
	if _, err := ApplyPatch(base, sections); err != nil {
		return nil, err
	}
	return s, nil
}

// ReadResult reads a published change directory: exactly change.patch and
// summary.json, both bounded regular files, verified against each other.
func ReadResult(dir string) (*Summary, []byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if len(entries) != 2 || !names[PatchFile] || !names[SummaryFile] {
		return nil, nil, errors.New("result directory must contain exactly change.patch and summary.json")
	}
	patch, err := capture.ReadRootFile(root, PatchFile, MaxPatchBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("change.patch: %w", err)
	}
	data, err := capture.ReadRootFile(root, SummaryFile, capture.MaxFileBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("summary.json: %w", err)
	}
	s, err := ParsePublishedChange(data, patch)
	if err != nil {
		return nil, nil, err
	}
	return s, patch, nil
}
