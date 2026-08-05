package casespec_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

func TestLoadGradingReadsBothEndsOfAWithheldSpec(t *testing.T) {
	corpus := t.TempDir()
	caseDir := filepath.Join(corpus, "fix-test-001")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `
schema: benchmark-case/v1
id: fix-test-001
workflow: small-fix
signature:
  inputs: {repository: repository/v1}
  outputs: {change: repository-change/v1}
pre_state:
  repository: {repo: jetbridge, ref: abc}
grading:
  fail_to_pass:
    - cmd: "go test ./pkg/ -count=1"
      withheld_tests:
        - source: ground_truth/withheld_tests/pkg/thing_test.go
          destination: pkg/thing_test.go
  pass_to_pass:
    - cmd: "go test ./pkg/ -run Guard -count=1"
      withheld_tests:
        - source: ground_truth/withheld_tests/pkg/thing_test.go
          destination: pkg/thing_test.go
`
	if err := os.WriteFile(filepath.Join(caseDir, "case.yaml"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	grading, err := casespec.LoadGrading(corpus, "fix-test-001")
	if err != nil {
		t.Fatalf("LoadGrading() = %v", err)
	}
	for name, legs := range map[string][]casespec.Leg{
		"fail_to_pass": grading.FailToPass, "pass_to_pass": grading.PassToPass,
	} {
		if len(legs) != 1 || len(legs[0].WithheldTests) != 1 {
			t.Fatalf("%s: got %+v", name, legs)
		}
		spec := legs[0].WithheldTests[0]
		if spec.Source != "ground_truth/withheld_tests/pkg/thing_test.go" || spec.Destination != "pkg/thing_test.go" {
			t.Fatalf("%s: spec = %+v", name, spec)
		}
	}
}
