package specparser

import (
	"fmt"
	"regexp"
	"strings"
)

// Requirement is a numbered requirement from a spec.
type Requirement struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// AcceptanceCriterion is a checkbox item from the acceptance criteria section.
type AcceptanceCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SpecItem is a unified item (requirement or acceptance criterion) for mapping.
type SpecItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Kind string `json:"kind"` // "requirement" or "acceptance_criterion"
}

// Spec is the parsed representation of a Markdown specification.
type Spec struct {
	Requirements       []Requirement         `json:"requirements"`
	AcceptanceCriteria []AcceptanceCriterion  `json:"acceptance_criteria"`
}

// AllItems returns a unified list of all spec items for mapping.
func (s *Spec) AllItems() []SpecItem {
	var items []SpecItem
	for _, r := range s.Requirements {
		items = append(items, SpecItem{ID: r.ID, Text: r.Text, Kind: "requirement"})
	}
	for _, ac := range s.AcceptanceCriteria {
		items = append(items, SpecItem{ID: ac.ID, Text: ac.Text, Kind: "acceptance_criterion"})
	}
	return items
}

var (
	numberedReqRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	checkboxRe    = regexp.MustCompile(`^-\s+\[[ xX]\]\s+(.+)$`)
	sectionRe     = regexp.MustCompile(`^##\s+(.+)$`)
)

// ParseSpec extracts requirements and acceptance criteria from Markdown.
func ParseSpec(markdownBytes []byte) (*Spec, error) {
	spec := &Spec{}
	lines := strings.Split(string(markdownBytes), "\n")

	currentSection := ""
	reqCount := 0
	acCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect section headers
		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			sectionName := strings.ToLower(strings.TrimSpace(m[1]))
			if strings.Contains(sectionName, "requirement") {
				currentSection = "requirements"
			} else if strings.Contains(sectionName, "acceptance") {
				currentSection = "acceptance_criteria"
			} else {
				currentSection = ""
			}
			continue
		}

		switch currentSection {
		case "requirements":
			if m := numberedReqRe.FindStringSubmatch(trimmed); m != nil {
				reqCount++
				spec.Requirements = append(spec.Requirements, Requirement{
					ID:   fmt.Sprintf("R%d", reqCount),
					Text: strings.TrimSpace(m[1]),
				})
			}
		case "acceptance_criteria":
			if m := checkboxRe.FindStringSubmatch(trimmed); m != nil {
				acCount++
				spec.AcceptanceCriteria = append(spec.AcceptanceCriteria, AcceptanceCriterion{
					ID:   fmt.Sprintf("AC%d", acCount),
					Text: strings.TrimSpace(m[1]),
				})
			}
		}
	}

	return spec, nil
}
