package runner

import (
	"strings"
	"testing"
)

// These tests live in the internal (package runner) test file, not
// runner_test.go, because verifyCLISupportsFlags is unexported and
// runner_test.go is package runner_test (external, black-box tests of
// Run()). There is no exported API for this check — it is an internal
// preflight, not a public capability — so the internal test file is where
// it can be exercised directly.

func TestUnsupportedBudgetFlagIsReportedAsImageSkew(t *testing.T) {
	help := "Usage: claude [options]\n  --max-turns <n>\n  --model <m>\n"
	err := verifyCLISupportsFlags(help, []string{"--max-turns", "--max-budget-usd"})
	if err == nil {
		t.Fatal("missing flag was accepted")
	}
	if !strings.Contains(err.Error(), "--max-budget-usd") || !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("error does not name the flag and the remedy: %v", err)
	}
}

func TestSupportedFlagsPassVerification(t *testing.T) {
	help := "Usage: claude [options]\n  --max-turns <n>\n  --max-budget-usd <usd>\n"
	if err := verifyCLISupportsFlags(help, []string{"--max-turns", "--max-budget-usd"}); err != nil {
		t.Fatal(err)
	}
}
