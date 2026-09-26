package implement

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/capture"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed validation.schema.json
var validationSchema string

const (
	// ValidationFile is the one file a published validation holds.
	ValidationFile = "validation.json"
	// ValidationVersion names the validation format.
	ValidationVersion = "implement-validation/v1"
	// MaxValidationBytes bounds validation.json. The template keeps the last
	// 64 KiB of the command's output, which JSON escaping can at most double.
	MaxValidationBytes = 1 << 20
)

// Validation outcomes. A command's failure is recorded, not raised: the Run
// publishes its results all or nothing, so the template's validate task
// succeeds whenever it could record what happened.
const (
	ValidationPassed     = "passed"
	ValidationFailed     = "failed"
	ValidationNotApplied = "not_applied"
)

func ValidationSchema() []byte { return []byte(validationSchema) }

var compiledValidation = jsonschema.MustCompileString("validation.json", validationSchema)

// Validation is validation.json, implement-validation/v1: what the operator's
// command did to the snapshot's base with the change applied. It is written by
// the template, never by the agent, and names the exact input and patch it
// ran against.
type Validation struct {
	SchemaVersion string `json:"schema_version"`
	RunID         int    `json:"run_id"`
	InputDigest   string `json:"input_digest"`
	PatchDigest   string `json:"patch_digest"`
	Applied       bool   `json:"applied"`
	Command       string `json:"command"`
	ExitCode      *int   `json:"exit_code"`
	Outcome       string `json:"outcome"`
	LogTail       string `json:"log_tail"`
}

// ParseValidation checks a published validation on its own: bounded, schema
// valid, and an outcome consistent with whether the patch applied and how
// the command exited. The command's output is arbitrary bytes cut at a byte
// boundary, so invalid UTF-8 is replaced rather than refused.
func ParseValidation(data []byte) (*Validation, error) {
	if len(data) > MaxValidationBytes {
		return nil, errors.New("validation.json is oversized")
	}
	data = bytes.ToValidUTF8(data, []byte("�"))
	var raw any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&raw); err != nil {
		return nil, errors.New("invalid validation JSON")
	}
	if err := compiledValidation.Validate(raw); err != nil {
		return nil, errors.New("validation does not conform to the JSON schema")
	}
	var v Validation
	if err := capture.DecodeStrict(data, &v); err != nil {
		return nil, fmt.Errorf("validation: %w", err)
	}
	switch {
	case v.Outcome == ValidationNotApplied:
		if v.Applied || v.ExitCode != nil {
			return nil, errors.New("validation reports a command result for a patch that did not apply")
		}
	case !v.Applied || v.ExitCode == nil:
		return nil, errors.New("validation reports an outcome without applying the patch and running the command")
	case (*v.ExitCode == 0) != (v.Outcome == ValidationPassed):
		return nil, errors.New("validation outcome does not match the command's exit code")
	}
	return &v, nil
}

// Attests binds a validation to a change: it must name the same Run, the same
// snapshot and the exact patch the change carries.
func (v *Validation) Attests(s *Summary, patch []byte) error {
	if s.RunID == nil || *s.RunID != v.RunID {
		return errors.New("validation and change belong to different Runs")
	}
	if v.InputDigest != s.Provenance.InputDigest {
		return errors.New("validation ran against a different snapshot than the change")
	}
	if v.PatchDigest != s.PatchDigest || v.PatchDigest != capture.Digest(patch) {
		return errors.New("validation patch digest does not match the change")
	}
	return nil
}

// Markdown renders the validation for a person.
func (v *Validation) Markdown() string {
	var b strings.Builder
	v.markdown(&b, "#")
	return b.String()
}

func (v *Validation) markdown(b *strings.Builder, level string) {
	outcome := strings.ReplaceAll(v.Outcome, "_", " ")
	fmt.Fprintf(b, "%s Validation: %s\n\n", level, outcome)
	fmt.Fprintf(b, "Run: %d\n\nInput: %s\n\nPatch: %s\n\n", v.RunID, v.InputDigest, v.PatchDigest)
	switch {
	case !v.Applied:
		b.WriteString("The patch did not apply to the snapshot's base, so the command did not run.\n\n")
	default:
		fmt.Fprintf(b, "Exit code: %d\n\n", *v.ExitCode)
	}
	b.WriteString("Command:\n\n")
	writeFence(b, "sh", v.Command)
	b.WriteString("\nOutput (last 64 KiB):\n\n")
	writeFence(b, "text", v.LogTail)
}

func writeFence(b *strings.Builder, info, text string) {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	fmt.Fprintf(b, "%s%s\n%s", fence, info, text)
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n")
}
