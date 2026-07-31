// Package reviewgrade scores a produced review/v1 record against a bench
// corpus expected-findings/v1 oracle.
package reviewgrade

import (
	"fmt"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Expected is one parsed expected-findings/v1 oracle file.
type Expected struct {
	Schema   string            `yaml:"schema"`
	Case     string            `yaml:"case"`
	Findings []ExpectedFinding `yaml:"findings"`
}

// ExpectedFinding is one human-authored ground-truth defect.
type ExpectedFinding struct {
	ID       string `yaml:"id"`
	Required bool   `yaml:"required"`
	Severity string `yaml:"severity"`
	Class    string `yaml:"class"`
	Title    string `yaml:"title"`
	File     string `yaml:"file"`
	Region   Region `yaml:"region"`
}

// Region is the anchored location of an expected finding.
type Region struct {
	Anchor string     `yaml:"anchor"`
	Lines  string     `yaml:"lines"`
	Also   []AlsoSite `yaml:"also"`
}

// AlsoSite is an additional acceptable location for the same defect.
type AlsoSite struct {
	File   string `yaml:"file"`
	Anchor string `yaml:"anchor"`
	Lines  string `yaml:"lines"`
}

// Site is a resolved file+line-range location. Start == 0 means the oracle
// gave no parseable range, so the whole file is acceptable.
type Site struct {
	File  string
	Start int
	End   int
}

// ParseExpected decodes an expected-findings/v1 document.
func ParseExpected(raw []byte) (*Expected, error) {
	var expected Expected
	if err := yaml.Unmarshal(raw, &expected); err != nil {
		return nil, fmt.Errorf("parse expected findings: %w", err)
	}
	if expected.Schema != "expected-findings/v1" {
		return nil, fmt.Errorf("parse expected findings: schema is %q, want expected-findings/v1", expected.Schema)
	}
	if len(expected.Findings) == 0 {
		return nil, fmt.Errorf("parse expected findings: no findings")
	}
	for index, finding := range expected.Findings {
		if finding.ID == "" {
			return nil, fmt.Errorf("parse expected findings: findings[%d] has no id", index)
		}
	}
	return &expected, nil
}

// Required returns only the findings recall is scored over.
func (expected Expected) Required() []ExpectedFinding {
	required := make([]ExpectedFinding, 0, len(expected.Findings))
	for _, finding := range expected.Findings {
		if finding.Required {
			required = append(required, finding)
		}
	}
	return required
}

// Sites resolves the primary location plus every `also` location.
func (finding ExpectedFinding) Sites() []Site {
	sites := make([]Site, 0, 1+len(finding.Region.Also))
	if finding.File != "" {
		start, end := parseLineRange(finding.Region.Lines)
		sites = append(sites, Site{File: finding.File, Start: start, End: end})
	}
	for _, also := range finding.Region.Also {
		if also.File == "" {
			continue
		}
		start, end := parseLineRange(also.Lines)
		sites = append(sites, Site{File: also.File, Start: start, End: end})
	}
	return sites
}

// rangeExpr matches a leading "N-M" or bare "N", tolerating trailing prose
// such as "482-498 (pre-state)".
var rangeExpr = regexp.MustCompile(`^\s*(\d+)\s*(?:-\s*(\d+))?`)

// parseLineRange returns (0, 0) when the oracle's line text is prose the
// grader cannot pin, which callers treat as a file-level match.
func parseLineRange(text string) (int, int) {
	match := rangeExpr.FindStringSubmatch(text)
	if match == nil {
		return 0, 0
	}
	start, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0
	}
	if match[2] == "" {
		return start, start
	}
	end, err := strconv.Atoi(match[2])
	if err != nil || end < start {
		return start, start
	}
	return start, end
}
