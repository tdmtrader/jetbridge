package review

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed review.schema.json
var reportSchema string

//go:embed review-profile.md
var defaultProfile string

func Schema() []byte         { return []byte(reportSchema) }
func DefaultProfile() []byte { return []byte(defaultProfile) }

// AssessmentSchema derives the model contract from the report's single schema.
// Provenance, verdict and durable identity are supplied only by the harness.
func AssessmentSchema() []byte {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		panic(err)
	}
	defs := root["$defs"].(map[string]any)
	assessment := defs["assessment"].(map[string]any)
	delete(defs, "assessment")
	assessment["$defs"] = defs
	assessment["$schema"] = root["$schema"]
	b, err := json.Marshal(assessment)
	if err != nil {
		panic(err)
	}
	return b
}

var (
	compiledReport     = jsonschema.MustCompileString("review.json", reportSchema)
	compiledAssessment = jsonschema.MustCompileString("assessment.json", string(AssessmentSchema()))
)

type Location struct {
	Side      string `json:"side"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
type Finding struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	Dimension      string   `json:"dimension"`
	Title          string   `json:"title"`
	Explanation    string   `json:"explanation"`
	Recommendation string   `json:"recommendation"`
	Location       Location `json:"location"`
}
type Assessment struct {
	Summary       string    `json:"summary"`
	Complete      bool      `json:"complete"`
	ReviewedFiles []string  `json:"reviewed_files"`
	Limitations   []string  `json:"limitations"`
	Findings      []Finding `json:"findings"`
}
type Provenance struct {
	InputDigest     string  `json:"input_digest"`
	BaseCommit      string  `json:"base_commit"`
	HeadCommit      string  `json:"head_commit"`
	ProfileDigest   string  `json:"profile_digest"`
	ModelRequested  string  `json:"model_requested"`
	ModelReported   *string `json:"model_reported"`
	CodexVersion    string  `json:"codex_version"`
	ExecutionPolicy string  `json:"execution_policy"`
}
type Report struct {
	SchemaVersion string     `json:"schema_version"`
	RunID         *int       `json:"run_id"`
	Verdict       string     `json:"verdict"`
	Provenance    Provenance `json:"provenance"`
	Assessment    Assessment `json:"assessment"`
}
type Metadata struct {
	RunID          *int
	ModelRequested string
	ModelReported  *string
	CodexVersion   string
	Profile        []byte
}

func decodeSchema(data []byte, schema *jsonschema.Schema, value any) error {
	if len(data) > maxFileBytes || !utf8.Valid(data) {
		return errors.New("invalid or oversized review JSON")
	}
	var raw any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&raw); err != nil {
		return errors.New("invalid review JSON")
	}
	if err := schema.Validate(raw); err != nil {
		return errors.New("review does not conform to the JSON schema")
	}
	return decodeStrict(data, value)
}

func BuildReport(bundle *Bundle, data []byte, meta Metadata) (*Report, error) {
	var assessment Assessment
	if err := decodeSchema(data, compiledAssessment, &assessment); err != nil {
		return nil, err
	}
	missing, err := validateAssessment(bundle, &assessment)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		assessment.Complete = false
		assessment.Limitations = append(assessment.Limitations, "Changed files not listed as reviewed: "+strings.Join(missing, ", "))
	}
	assessment.Limitations = append(assessment.Limitations, "Tests and repository code were not executed.")
	if bundle.Manifest.PlanDigest == nil {
		assessment.Limitations = append(assessment.Limitations, "No plan was supplied; plan compliance was not assessed.")
	}
	for i := range assessment.Findings {
		assessment.Findings[i].ID = fmt.Sprintf("f-%03d", i+1)
	}
	r := &Report{SchemaVersion: "review/v1", RunID: meta.RunID, Assessment: assessment, Provenance: Provenance{
		InputDigest: bundle.Digest, BaseCommit: bundle.Manifest.BaseCommit, HeadCommit: bundle.Manifest.HeadCommit,
		ProfileDigest: digest(meta.Profile), ModelRequested: meta.ModelRequested, ModelReported: meta.ModelReported,
		CodexVersion: meta.CodexVersion, ExecutionPolicy: "inspect-only",
	}}
	r.Verdict = verdict(assessment)
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return ParseReport(b, bundle)
}

func verdict(a Assessment) string {
	if !a.Complete {
		return "incomplete"
	}
	if len(a.Findings) > 0 {
		return "findings"
	}
	return "no_findings"
}

// ParsePublishedReport checks the persisted wire contract and harness-assigned
// identities. Source coverage is checked by ParseReport when the input bundle
// is available; a remote reader relies on the publishing worker for that check.
func ParsePublishedReport(data []byte) (*Report, error) {
	var r Report
	if err := decodeSchema(data, compiledReport, &r); err != nil {
		return nil, err
	}
	if r.Verdict != verdict(r.Assessment) {
		return nil, errors.New("verdict does not match assessment")
	}
	for i, f := range r.Assessment.Findings {
		if f.ID != fmt.Sprintf("f-%03d", i+1) {
			return nil, errors.New("invalid canonical finding identity")
		}
	}
	return &r, nil
}

func ParseReport(data []byte, bundle *Bundle) (*Report, error) {
	r, err := ParsePublishedReport(data)
	if err != nil {
		return nil, err
	}
	p := r.Provenance
	if p.InputDigest != bundle.Digest || p.BaseCommit != bundle.Manifest.BaseCommit || p.HeadCommit != bundle.Manifest.HeadCommit {
		return nil, errors.New("report provenance does not match the input")
	}
	missing, err := validateAssessment(bundle, &r.Assessment)
	if err != nil {
		return nil, err
	}
	if r.Assessment.Complete && len(missing) > 0 {
		return nil, errors.New("verdict does not match review coverage")
	}
	return r, nil
}

func (b *Bundle) changedFiles() map[string]bool {
	sides := map[string]map[string]File{"base": {}, "head": {}}
	for _, f := range b.Manifest.Files {
		sides[f.Side][f.Path] = f
	}
	changed := map[string]bool{}
	for side, files := range sides {
		other := "base"
		if side == "base" {
			other = "head"
		}
		for name, f := range files {
			peer, ok := sides[other][name]
			if !ok || peer.Digest != f.Digest || peer.Mode != f.Mode {
				changed[name] = true
			}
		}
	}
	return changed
}

func validateAssessment(b *Bundle, a *Assessment) ([]string, error) {
	if strings.TrimSpace(a.Summary) == "" {
		return nil, errors.New("empty summary")
	}
	files := map[string]File{}
	paths := map[string]bool{}
	for _, f := range b.Manifest.Files {
		files[f.Side+"/"+f.Path] = f
		paths[f.Path] = true
	}
	reviewed := map[string]bool{}
	for _, name := range a.ReviewedFiles {
		if !safePath(name) || !paths[name] || reviewed[name] {
			return nil, errors.New("reviewed_files contains an invalid or duplicate path")
		}
		reviewed[name] = true
	}
	changed := b.changedFiles()
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, f := range a.Findings {
		loc := f.Location
		if strings.TrimSpace(f.Title) == "" || strings.TrimSpace(f.Explanation) == "" || strings.TrimSpace(f.Recommendation) == "" {
			return nil, errors.New("empty finding text")
		}
		file, exists := files[loc.Side+"/"+loc.Path]
		if !safePath(loc.Path) || !exists || !changed[loc.Path] || !reviewed[loc.Path] {
			return nil, errors.New("finding must locate an inspected changed file")
		}
		data, err := readRootFile(root, loc.Side+"/"+loc.Path, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if digest(data) != file.Digest {
			return nil, errors.New("source changed during review")
		}
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return nil, errors.New("line locations require text content")
		}
		lines := bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if loc.StartLine < 1 || loc.EndLine < loc.StartLine || loc.EndLine > lines {
			return nil, errors.New("finding line range is outside source")
		}
	}
	missing := []string{}
	for name := range changed {
		if !reviewed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// Markdown renders the report itself; there is no separately authored narrative.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Code review: %s\n\n%s\n\n", r.Verdict, markdownText(r.Assessment.Summary))
	fmt.Fprintf(&b, "Base: %s\n\nHead: %s\n\n", r.Provenance.BaseCommit, r.Provenance.HeadCommit)
	for _, f := range r.Assessment.Findings {
		fmt.Fprintf(&b, "## %s · %s · %s\n\n%s\n\n%s\n\nLocation: %s/%s:%d–%d\n\nRecommendation: %s\n\n",
			f.ID, f.Severity, f.Dimension, markdownText(f.Title), markdownText(f.Explanation), f.Location.Side, markdownText(f.Location.Path), f.Location.StartLine, f.Location.EndLine, markdownText(f.Recommendation))
	}
	b.WriteString("## Limitations\n\n")
	for _, s := range r.Assessment.Limitations {
		fmt.Fprintf(&b, "- %s\n", markdownText(s))
	}
	b.WriteString("\n## Reviewed files\n\n")
	for _, s := range r.Assessment.ReviewedFiles {
		fmt.Fprintf(&b, "- %s\n", markdownText(s))
	}
	return b.String()
}

func markdownText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "#", "\\#").Replace(html.EscapeString(s))
}
