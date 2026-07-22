package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestQuestionAndHumanAnswerContractsValidateImmutableInteraction(t *testing.T) {
	question := contracts.QuestionDocument{
		SchemaVersion: "1.0.0", Prompt: "Deploy to production?", Context: "Release 42 passed staging.",
		Options: []string{"yes", "no"}, Default: "no",
	}
	if _, err := validateFiles(t, "question/v1", map[string][]byte{"question.json": marshalDocument(t, question)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid question error = %v", err)
	}

	answer := contracts.HumanAnswerDocument{
		SchemaVersion: "1.0.0", Answer: "yes",
		AnsweredBy: "alice@example.com", AnsweredAt: "2026-07-22T12:00:00Z",
	}
	if _, err := validateFiles(t, "human-answer/v1", map[string][]byte{"human-answer.json": marshalDocument(t, answer)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid human answer error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(*contracts.HumanAnswerDocument)
		want  string
	}{
		{"blank answer", func(d *contracts.HumanAnswerDocument) { d.Answer = " " }, "answer"},
		{"blank actor", func(d *contracts.HumanAnswerDocument) { d.AnsweredBy = " " }, "answered_by"},
		{"invalid time", func(d *contracts.HumanAnswerDocument) { d.AnsweredAt = "now" }, "answered_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := answer
			tc.setup(&document)
			if _, err := validateFiles(t, "human-answer/v1", map[string][]byte{"human-answer.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
	timedOut := answer
	timedOut.TimedOut = true
	timedOut.Answer = ""
	if _, err := validateFiles(t, "human-answer/v1", map[string][]byte{"human-answer.json": marshalDocument(t, timedOut)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("timed-out empty answer error = %v", err)
	}
}

func TestQuestionContractValidatesOptionsAndDefault(t *testing.T) {
	valid := contracts.QuestionDocument{SchemaVersion: "1.0.0", Prompt: "Choose", Context: "context", Options: []string{"a", "b"}, Default: "a"}
	for _, tc := range []struct {
		name  string
		setup func(*contracts.QuestionDocument)
		want  string
	}{
		{"duplicate option", func(d *contracts.QuestionDocument) { d.Options[1] = "a" }, "duplicate"},
		{"blank option", func(d *contracts.QuestionDocument) { d.Options[1] = " " }, "options"},
		{"default not an option", func(d *contracts.QuestionDocument) { d.Default = "c" }, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := valid
			document.Options = append([]string(nil), valid.Options...)
			tc.setup(&document)
			if _, err := validateFiles(t, "question/v1", map[string][]byte{"question.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInteractionContractsUseExactVersionAndStrictJSON(t *testing.T) {
	question := []byte(`{"schema_version":"1.0.1","prompt":"p","context":"c"}`)
	if _, err := validateFiles(t, "question/v1", map[string][]byte{"question.json": question}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("version error = %v, want exact version", err)
	}
	unknown := []byte(`{"schema_version":"1.0.0","prompt":"p","context":"c","workflow_run_id":"1"}`)
	if _, err := validateFiles(t, "question/v1", map[string][]byte{"question.json": unknown}, emptyValidationContext(t)); err == nil {
		t.Fatal("mutable workflow ID field was accepted")
	}
}
