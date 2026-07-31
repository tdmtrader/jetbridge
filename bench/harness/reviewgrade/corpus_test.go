package reviewgrade_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

// The corpus's expected-findings oracles were hand-authored per case and never
// normalized: as of 2026-07-30 there are five dialects across six code-review
// cases (schema expected-findings/v1, expected-findings/v0 and
// review-findings/v1; findings under `findings:` or `required:`; locations
// under file+region, anchors[], primary_anchor, or supporting_regions[]).
//
// Rather than rewrite withheld human ground truth, the parser normalizes every
// dialect into Site. This test pins that against the real files, so a new case
// in a sixth dialect fails here rather than silently scoring zero recall.
func TestEveryCorpusOracleParsesAndAnchors(t *testing.T) {
	paths, err := filepath.Glob("../../corpus/*/ground_truth/expected_findings.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no corpus oracles found - is the relative path still right?")
	}

	for _, path := range paths {
		caseID := filepath.Base(filepath.Dir(filepath.Dir(path)))
		t.Run(caseID, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			oracle, err := reviewgrade.ParseExpected(raw)
			if err != nil {
				t.Fatalf("ParseExpected: %v", err)
			}
			required := oracle.Required()
			if len(required) == 0 {
				t.Fatalf("no required findings parsed; recall would be meaningless")
			}
			for _, finding := range required {
				if finding.ID == "" {
					t.Errorf("a required finding has no id")
				}
				sites := finding.Sites()
				if len(sites) == 0 {
					t.Errorf("required finding %q resolved to no anchor sites", finding.ID)
					continue
				}
				for _, site := range sites {
					if site.File == "" {
						t.Errorf("finding %q produced a site with no file: %#v", finding.ID, site)
					}
				}
			}
			t.Logf("%s: %d required findings, %d total", caseID, len(required), len(oracle.Findings))
		})
	}
}
