// Package features binds each Scenario title to a Ginkgo It of the same text
// in queue/*/ (core, config), since this module carries no Gherkin runner.
package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, pattern string) string {
	files, _ := filepath.Glob(pattern)
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString(string(b) + "\n")
	}
	return all.String()
}

func TestEveryScenarioHasAMatchingIt(t *testing.T) {
	specs, scenarios := read(t, "../*/*_test.go"), 0
	for _, line := range strings.Split(read(t, "*.feature"), "\n") {
		title, ok := strings.CutPrefix(strings.TrimSpace(line), "Scenario:")
		if !ok {
			continue
		}
		scenarios++
		if want := `It("` + strings.TrimSpace(title) + `"`; !strings.Contains(specs, want) {
			t.Errorf("scenario %q has no %s) in queue/", strings.TrimSpace(title), want)
		}
	}
	if scenarios == 0 {
		t.Fatal("no scenarios found in queue/features/*.feature")
	}
}
