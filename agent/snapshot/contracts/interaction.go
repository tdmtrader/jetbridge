package contracts

import (
	"fmt"
	"strings"
	"time"
)

type QuestionDocument struct {
	SchemaVersion string   `json:"schema_version"`
	Prompt        string   `json:"prompt"`
	Context       string   `json:"context"`
	Options       []string `json:"options,omitempty"`
	Default       string   `json:"default,omitempty"`
}

func (d QuestionDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"prompt", d.Prompt}, {"context", d.Context}}); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(d.Options))
	for i, option := range d.Options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("options[%d] is required", i)
		}
		if _, found := seen[option]; found {
			return fmt.Errorf("options[%d] %q is duplicate", i, option)
		}
		seen[option] = struct{}{}
	}
	if d.Default != "" {
		if _, found := seen[d.Default]; !found {
			return fmt.Errorf("default must match one of options")
		}
	}
	return nil
}

type HumanAnswerDocument struct {
	SchemaVersion string `json:"schema_version"`
	Answer        string `json:"answer"`
	AnsweredBy    string `json:"answered_by"`
	AnsweredAt    string `json:"answered_at"`
	TimedOut      bool   `json:"timed_out,omitempty"`
}

func (d HumanAnswerDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{
		{"answered_by", d.AnsweredBy}, {"answered_at", d.AnsweredAt},
	}); err != nil {
		return err
	}
	if !d.TimedOut && strings.TrimSpace(d.Answer) == "" {
		return fmt.Errorf("answer is required when timed_out is false")
	}
	if _, err := time.Parse(time.RFC3339, d.AnsweredAt); err != nil {
		return fmt.Errorf("answered_at must be RFC3339: %w", err)
	}
	return nil
}
