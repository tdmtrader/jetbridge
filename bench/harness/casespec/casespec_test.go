package casespec_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

const corpus = "../../corpus"

// The three cases sharing the {repository, change, work-item} -> review/v1
// signature the bench/nodes/code-review node implements.
var codeReviewCases = []string{"review-jb-001", "review-jb-004", "neg-cc-001"}

func TestLoadResolvesEveryCodeReviewCase(t *testing.T) {
	for _, caseID := range codeReviewCases {
		t.Run(caseID, func(t *testing.T) {
			spec, err := casespec.Load(corpus, caseID)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, port := range []string{"repository", "change", "work-item"} {
				typeRef, found := spec.Inputs[port]
				if !found {
					t.Fatalf("input %q missing; inputs = %v", port, spec.Inputs)
				}
				if typeRef == "" {
					t.Fatalf("input %q has no type", port)
				}
				if _, found := spec.Ports[port]; !found {
					t.Fatalf("pre_state entry for %q missing", port)
				}
			}
			if spec.Inputs["review"] != "" {
				t.Fatalf("review is an output, not an input")
			}
		})
	}
}

// review-jb-004 spells its repository port as an inline flow map
// (`repository: {repo: jetbridge, ref: c4d9...}`). A line-oriented parser reads
// the ref as "{repo:" and silently materializes the wrong tree.
func TestLoadReadsInlineFlowMapRefs(t *testing.T) {
	spec, err := casespec.Load(corpus, "review-jb-004")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	repository := spec.Ports["repository"]
	if repository.Ref != "c4d9fcb91412ae3be80c34a7e6d1fedf3f9bb355" {
		t.Fatalf("repository ref = %q", repository.Ref)
	}
	if !repository.SourceTree() {
		t.Fatal("repository must be a source tree port")
	}
}

// The exposed change file is not always task/change.diff: neg-cc-001 ships
// task/change-under-review.diff. The path must come from case.yaml.
func TestLoadReadsPerCaseChangePaths(t *testing.T) {
	for caseID, want := range map[string]string{
		"review-jb-004": "task/change.diff",
		"review-jb-001": "task/change.diff",
		"neg-cc-001":    "task/change-under-review.diff",
	} {
		spec, err := casespec.Load(corpus, caseID)
		if err != nil {
			t.Fatalf("%s: Load: %v", caseID, err)
		}
		if got := spec.Ports["change"].Path; got != want {
			t.Errorf("%s: change path = %q, want %q", caseID, got, want)
		}
	}
}

// Cases whose answer key is reachable from branch refs carry an explicit
// materialize: directive. Losing it means materializing a tree the solver can
// walk forward from into the terminal commit.
func TestLoadPreservesMaterializeDirectives(t *testing.T) {
	spec, err := casespec.Load(corpus, "review-jb-001")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if spec.Ports["repository"].Materialize == "" {
		t.Fatal("review-jb-001 declares a materialize directive; it must survive parsing")
	}
}

func TestLoadRejectsAnInputWithNoPreState(t *testing.T) {
	if _, err := casespec.Load(corpus, "no-such-case"); err == nil {
		t.Fatal("expected an error for a missing case")
	}
}
