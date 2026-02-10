package mapper

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/concourse/ci-agent/specparser"
)

// TestEntry represents a discovered test in the codebase.
type TestEntry struct {
	File        string `json:"file"`
	Function    string `json:"function"`
	Description string `json:"description"`
}

// TestIndex is a scanned index of test files and their test functions.
type TestIndex struct {
	Tests []TestEntry `json:"tests"`
}

// RequirementMapping links a spec item to matched tests.
type RequirementMapping struct {
	SpecItem specparser.SpecItem `json:"spec_item"`
	Matches  []TestMatch         `json:"matches"`
	Status   string              `json:"status"` // covered, partial, uncovered
}

// TestMatch is a matched test with confidence score.
type TestMatch struct {
	Test       TestEntry `json:"test"`
	Confidence float64   `json:"confidence"`
}

var (
	funcTestRe = regexp.MustCompile(`func\s+(Test\w+)\s*\(`)
	itBlockRe  = regexp.MustCompile(`It\(\s*"([^"]+)"`)
	describeRe = regexp.MustCompile(`Describe\(\s*"([^"]+)"`)
	contextRe  = regexp.MustCompile(`Context\(\s*"([^"]+)"`)
)

// BuildTestIndex scans a repo for _test.go files and extracts test functions.
func BuildTestIndex(repoDir string, includes []string) (*TestIndex, error) {
	index := &TestIndex{}

	err := filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, _ := filepath.Rel(repoDir, path)

		if len(includes) > 0 {
			matched := false
			for _, pattern := range includes {
				if m, _ := filepath.Match(pattern, relPath); m {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		text := string(content)

		// Extract func Test... names
		for _, m := range funcTestRe.FindAllStringSubmatch(text, -1) {
			index.Tests = append(index.Tests, TestEntry{
				File:     relPath,
				Function: m[1],
			})
		}

		// Extract Ginkgo It() descriptions
		for _, m := range itBlockRe.FindAllStringSubmatch(text, -1) {
			index.Tests = append(index.Tests, TestEntry{
				File:        relPath,
				Description: m[1],
			})
		}

		// Extract Ginkgo Describe() descriptions
		for _, m := range describeRe.FindAllStringSubmatch(text, -1) {
			index.Tests = append(index.Tests, TestEntry{
				File:        relPath,
				Description: m[1],
			})
		}

		return nil
	})

	return index, err
}

// MapRequirements maps spec items to discovered tests using keyword matching.
func MapRequirements(spec *specparser.Spec, index *TestIndex) []RequirementMapping {
	var mappings []RequirementMapping

	for _, item := range spec.AllItems() {
		mapping := RequirementMapping{
			SpecItem: item,
			Status:   "uncovered",
		}

		itemWords := significantWords(item.Text)

		for _, test := range index.Tests {
			testText := strings.ToLower(test.Function + " " + test.Description)
			matchCount := 0
			for _, w := range itemWords {
				if strings.Contains(testText, w) {
					matchCount++
				}
			}
			if len(itemWords) > 0 && matchCount > 0 {
				confidence := float64(matchCount) / float64(len(itemWords))
				mapping.Matches = append(mapping.Matches, TestMatch{
					Test:       test,
					Confidence: confidence,
				})
			}
		}

		if len(mapping.Matches) > 0 {
			bestConfidence := 0.0
			for _, m := range mapping.Matches {
				if m.Confidence > bestConfidence {
					bestConfidence = m.Confidence
				}
			}
			if bestConfidence >= 0.5 {
				mapping.Status = "covered"
			} else {
				mapping.Status = "partial"
			}
		}

		mappings = append(mappings, mapping)
	}

	return mappings
}

func significantWords(text string) []string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true,
		"that": true, "which": true, "who": true, "whom": true,
		"this": true, "these": true, "those": true,
		"and": true, "but": true, "or": true, "nor": true, "not": true,
		"so": true, "yet": true, "both": true, "either": true, "neither": true,
		"for": true, "with": true, "from": true, "to": true, "of": true,
		"in": true, "on": true, "at": true, "by": true,
	}

	var words []string
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}") // remove punctuation
		if len(w) > 2 && !stopWords[w] {
			words = append(words, w)
		}
	}
	return words
}
