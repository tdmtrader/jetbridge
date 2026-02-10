package implement

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// SpecContext holds the raw spec content and extracted acceptance criteria.
type SpecContext struct {
	Raw                string   `json:"raw"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}

var acRe = regexp.MustCompile(`^- \[[x ]\]\s+(.+)$`)

// ReadSpec reads a spec file and extracts acceptance criteria.
func ReadSpec(path string) (*SpecContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	raw := string(data)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("spec file is empty: %s", path)
	}

	ctx := &SpecContext{Raw: raw}

	// Extract acceptance criteria from the ## Acceptance Criteria section.
	inAC := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## Acceptance Criteria") {
			inAC = true
			continue
		}

		// Stop at next heading.
		if inAC && strings.HasPrefix(trimmed, "## ") {
			break
		}

		if inAC {
			if m := acRe.FindStringSubmatch(trimmed); m != nil {
				ctx.AcceptanceCriteria = append(ctx.AcceptanceCriteria, m[1])
			}
		}
	}

	return ctx, nil
}
