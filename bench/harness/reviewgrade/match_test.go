package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func anchor(path string, start, end int) reviewgrade.Anchor {
	return reviewgrade.Anchor{Locator: reviewgrade.Locator{Kind: "line-range", Path: path, Start: &start, End: &end}}
}

func expectedAt(file string, lines string) reviewgrade.ExpectedFinding {
	return reviewgrade.ExpectedFinding{
		ID: "F1", Required: true, File: file,
		Region: reviewgrade.Region{Lines: lines},
	}
}

func TestMatchesOnOverlappingRegionInSameFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 490, 495)}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("expected overlapping region to match")
	}
}

func TestDoesNotMatchDifferentFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/other.go", 490, 495)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("different file must not match")
	}
}

func TestDoesNotMatchDistantRegionInSameFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 900, 910)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("distant region must not match")
	}
}

func TestToleranceWidensTheAcceptedWindow(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 505, 507)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("must not match at zero tolerance")
	}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 10) {
		t.Fatal("must match at tolerance 10")
	}
}

func TestFileLevelOracleMatchesAnyLineInThatFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 5000, 5001)}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "throughout"), produced, 0) {
		t.Fatal("unparseable oracle lines must degrade to a file-level match")
	}
}

func TestProducedFindingWithoutLineNumbersMatchesOnPathAlone(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{{Locator: reviewgrade.Locator{Kind: "file", Path: "atc/db/x.go"}}}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("path-only evidence must match, credited generously")
	}
}

func TestMatchesViaAlsoSite(t *testing.T) {
	expected := reviewgrade.ExpectedFinding{
		ID: "F1", Required: true, File: "atc/db/x.go",
		Region: reviewgrade.Region{
			Lines: "482-498",
			Also:  []reviewgrade.AlsoSite{{File: "atc/runlifecycle/lifecycler.go", Lines: "81-105"}},
		},
	}
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/runlifecycle/lifecycler.go", 90, 92)}}
	if !reviewgrade.Matches(expected, produced, 0) {
		t.Fatal("an also-site must match")
	}
}

func TestFindingWithNoEvidenceNeverMatches(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a"}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("unanchored finding must not match")
	}
}
