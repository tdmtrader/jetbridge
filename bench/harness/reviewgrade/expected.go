// Package reviewgrade scores a produced review/v1 record against a bench
// corpus expected-findings/v1 oracle.
package reviewgrade

import (
	"fmt"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Expected is one parsed expected-findings oracle file.
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

	// resolved holds the sites normalized out of a parsed oracle. It is empty
	// for values constructed literally (tests), where Sites() falls back to
	// File/Region.
	resolved []Site
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

// knownSchemas are the oracle dialects observed in the corpus. The corpus's
// expected-findings files were hand-authored per case and never normalized;
// an empty schema key is one of the real shapes, not an error.
var knownSchemas = map[string]bool{
	"":                     true,
	"expected-findings/v1": true,
	"expected-findings/v0": true,
	"review-findings/v1":   true,
}

// ParseExpected decodes any of the corpus's expected-findings dialects,
// normalizing every location spelling into Site.
func ParseExpected(raw []byte) (*Expected, error) {
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse expected findings: %w", err)
	}

	expected := &Expected{
		Schema: text(document["schema"]),
		Case:   text(document["case"]),
	}
	if !knownSchemas[expected.Schema] {
		return nil, fmt.Errorf("parse expected findings: unknown schema %q", expected.Schema)
	}

	// Findings live under `findings:` in most dialects and `required:` in
	// review-ld-001. Take whichever is present.
	entries := list(document["findings"])
	if len(entries) == 0 {
		entries = list(document["required"])
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("parse expected findings: no findings")
	}

	for index, entry := range entries {
		mapping, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse expected findings: findings[%d] is not a mapping", index)
		}
		finding := normalizeFinding(mapping)
		if finding.ID == "" {
			return nil, fmt.Errorf("parse expected findings: findings[%d] has no id", index)
		}
		expected.Findings = append(expected.Findings, finding)
	}
	return expected, nil
}

// normalizeFinding flattens one oracle entry, in any dialect, into the common
// shape. A finding is required unless it explicitly says otherwise: dialects
// without a `required` key list only genuine defects.
func normalizeFinding(mapping map[string]any) ExpectedFinding {
	finding := ExpectedFinding{
		ID:       text(mapping["id"]),
		Required: true,
		Severity: text(mapping["severity"]),
		Title:    text(mapping["title"]),
		File:     text(mapping["file"]),
	}
	if finding.Class = text(mapping["class"]); finding.Class == "" {
		finding.Class = text(mapping["category"])
	}
	if required, ok := mapping["required"].(bool); ok {
		finding.Required = required
	}

	primaryFile := finding.File

	// file + region at the top level (review-jb-004, review-jb-003, neg-cc-001,
	// rca-jb-005).
	if primaryFile != "" {
		start, end := lineRange(mapping["region"])
		finding.resolved = append(finding.resolved, Site{File: primaryFile, Start: start, End: end})
	}

	// region.also[] (review-jb-004).
	if region, ok := mapping["region"].(map[string]any); ok {
		finding.resolved = append(finding.resolved, anchorSites(region["also"], primaryFile)...)
	}

	// Every observed sibling-location key.
	for _, key := range []string{
		"anchors",            // review-jb-001, feedback-jb-003, review-ld-001
		"supporting_regions", // review-jb-003
		"supporting_anchors", //
		"also",               //
		"additional_anchors", //
		"primary_anchor",     // review-jb-002
	} {
		finding.resolved = append(finding.resolved, anchorSites(mapping[key], primaryFile)...)
	}

	finding.resolved = dedupeSites(finding.resolved)
	return finding
}

// anchorSites accepts either a single anchor mapping or a list of them.
func anchorSites(value any, fallbackFile string) []Site {
	if value == nil {
		return nil
	}
	if mapping, ok := value.(map[string]any); ok {
		return anchorSite(mapping, fallbackFile)
	}
	var sites []Site
	for _, entry := range list(value) {
		if mapping, ok := entry.(map[string]any); ok {
			sites = append(sites, anchorSite(mapping, fallbackFile)...)
		}
	}
	return sites
}

// anchorSite resolves one anchor mapping. Line information may be spelled
// `lines` (string or [start, end]), `region` (string or {start, end}),
// `line` (a single int), or `start`/`end` directly on the anchor.
func anchorSite(mapping map[string]any, fallbackFile string) []Site {
	file := text(mapping["file"])
	if file == "" {
		file = fallbackFile
	}
	if file == "" {
		return nil
	}
	for _, key := range []string{"lines", "region", "line"} {
		if start, end := lineRange(mapping[key]); start != 0 {
			return []Site{{File: file, Start: start, End: end}}
		}
	}
	if start, end := boundsFrom(mapping); start != 0 {
		return []Site{{File: file, Start: start, End: end}}
	}
	return []Site{{File: file}}
}

// lineRange interprets any of the corpus's line spellings. It returns (0, 0)
// when the value is prose or absent, which callers treat as file-level.
func lineRange(value any) (int, int) {
	switch typed := value.(type) {
	case nil:
		return 0, 0
	case string:
		return parseLineRange(typed)
	case int:
		return typed, typed
	case float64:
		return int(typed), int(typed)
	case []any:
		var numbers []int
		for _, entry := range typed {
			if number, ok := asInt(entry); ok {
				numbers = append(numbers, number)
			}
		}
		if len(numbers) >= 2 {
			return numbers[0], numbers[1]
		}
		if len(numbers) == 1 {
			return numbers[0], numbers[0]
		}
		return 0, 0
	case map[string]any:
		if start, end := boundsFrom(typed); start != 0 {
			return start, end
		}
		for _, key := range []string{"lines", "region", "line"} {
			if start, end := lineRange(typed[key]); start != 0 {
				return start, end
			}
		}
		return 0, 0
	}
	return 0, 0
}

// boundsFrom reads an explicit {start, end} pair off a mapping.
func boundsFrom(mapping map[string]any) (int, int) {
	start, ok := asInt(mapping["start"])
	if !ok {
		return 0, 0
	}
	end, ok := asInt(mapping["end"])
	if !ok {
		end = start
	}
	return start, end
}

// rangeExpr matches a leading "N-M" or bare "N", tolerating an "L" line
// prefix ("L5-1015") and trailing prose ("482-498 (pre-state)").
var rangeExpr = regexp.MustCompile(`^\s*L?(\d+)\s*(?:-\s*L?(\d+))?`)

// parseLineRange returns (0, 0) when the oracle's line text is prose the
// grader cannot pin, which callers treat as a file-level match.
func parseLineRange(source string) (int, int) {
	match := rangeExpr.FindStringSubmatch(source)
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

func dedupeSites(sites []Site) []Site {
	if len(sites) < 2 {
		return sites
	}
	seen := make(map[Site]bool, len(sites))
	unique := sites[:0]
	for _, site := range sites {
		if seen[site] {
			continue
		}
		seen[site] = true
		unique = append(unique, site)
	}
	return unique
}

func text(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func list(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		return int(typed), true
	}
	return 0, false
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

// Sites resolves every acceptable location for this finding.
func (finding ExpectedFinding) Sites() []Site {
	if len(finding.resolved) > 0 {
		return finding.resolved
	}
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
