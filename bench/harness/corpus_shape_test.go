package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

// Every withheld spec in the corpus must name both ends explicitly. The corpus
// previously spelled them four different ways — mirrored repo-relative,
// case-relative with the destination only implied, restored by the leg's own
// cmd, and declared but shipped nowhere — and a grader that guesses a
// destination does not error, it silently scores a correct fix as a miss.
func TestEveryWithheldSpecNamesSourceAndDestination(t *testing.T) {
	corpus := filepath.Join("..", "corpus")
	entries, err := os.ReadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		caseID := entry.Name()
		if _, err := os.Stat(filepath.Join(corpus, caseID, "case.yaml")); err != nil {
			continue
		}
		grading, err := casespec.LoadGrading(corpus, caseID)
		if err != nil {
			t.Fatalf("%s: %v", caseID, err)
		}
		legs := append(append([]casespec.Leg{}, grading.FailToPass...), grading.PassToPass...)
		for index, leg := range legs {
			if len(leg.WithheldTestPaths) > 0 {
				t.Errorf("%s leg %d still uses the legacy withheld_test_paths spelling", caseID, index)
			}
			for _, spec := range leg.WithheldTests {
				if spec.Destination == "" {
					t.Errorf("%s leg %d: withheld spec needs a destination, got %+v",
						caseID, index, spec)
					continue
				}
				if spec.SelfRestoring {
					continue
				}
				if spec.Source == "" {
					t.Errorf("%s leg %d: withheld spec needs both source and destination, got %+v",
						caseID, index, spec)
					continue
				}
				source := filepath.Join(corpus, caseID, spec.Source)
				if _, err := os.Stat(source); err != nil {
					t.Errorf("%s leg %d: withheld source %s does not exist", caseID, index, spec.Source)
				}
			}
		}
	}
}
