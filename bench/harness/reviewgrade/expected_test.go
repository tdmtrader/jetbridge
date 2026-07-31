package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

const oracleYAML = `
schema: expected-findings/v1
case: review-jb-004
findings:
  - id: F1-linkage-unpinned
    required: true
    severity: major
    title: Destructive pipeline archival is driven by a caller-writable id
    file: atc/db/pipeline_run_factory.go
    region:
      anchor: "func terminalTicketLinkage() (string, []any)"
      lines: "482-498 (pre-state)"
      also:
        - {file: atc/runlifecycle/lifecycler.go, anchor: "Run()", lines: "81-105"}
  - id: F2-chip-noise
    required: false
    severity: minor
    title: Spend chip renders on non-agent builds
    file: web/elm/src/Build/Build.elm
    region:
      anchor: "viewSpendChip"
      lines: "1204"
`

func TestParseExpectedFindings(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	if oracle.Case != "review-jb-004" {
		t.Fatalf("Case = %q", oracle.Case)
	}
	if len(oracle.Findings) != 2 {
		t.Fatalf("len(Findings) = %d", len(oracle.Findings))
	}
	if !oracle.Findings[0].Required || oracle.Findings[1].Required {
		t.Fatalf("required flags = %v, %v", oracle.Findings[0].Required, oracle.Findings[1].Required)
	}
	if got := oracle.Required(); len(got) != 1 || got[0].ID != "F1-linkage-unpinned" {
		t.Fatalf("Required() = %#v", got)
	}
}

func TestExpectedFindingSitesIncludesPrimaryAndAlso(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[0].Sites()
	if len(sites) != 2 {
		t.Fatalf("len(sites) = %d: %#v", len(sites), sites)
	}
	if sites[0].File != "atc/db/pipeline_run_factory.go" || sites[0].Start != 482 || sites[0].End != 498 {
		t.Fatalf("sites[0] = %#v", sites[0])
	}
	if sites[1].File != "atc/runlifecycle/lifecycler.go" || sites[1].Start != 81 || sites[1].End != 105 {
		t.Fatalf("sites[1] = %#v", sites[1])
	}
}

func TestSingleLineRegionParsesAsPointRange(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[1].Sites()
	if len(sites) != 1 || sites[0].Start != 1204 || sites[0].End != 1204 {
		t.Fatalf("sites = %#v", sites)
	}
}

func TestUnparseableLinesYieldFileLevelSite(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(`
schema: expected-findings/v1
case: x
findings:
  - id: F1
    required: true
    file: a/b.go
    region: {anchor: "whatever", lines: "throughout"}
`))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[0].Sites()
	if len(sites) != 1 || sites[0].File != "a/b.go" || sites[0].Start != 0 || sites[0].End != 0 {
		t.Fatalf("sites = %#v", sites)
	}
}
