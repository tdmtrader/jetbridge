package main

import (
	"strings"
	"testing"
)

func TestVerifyRequiresActualPassingCasesInEveryManifest(t *testing.T) {
	local := "{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"one\",\"status\":\"passed\"}\n"
	live := "{\"type\":\"scenario_end\",\"manifest\":\"live\",\"name\":\"two\",\"status\":\"passed\"}\n"
	end := "{\"type\":\"run_end\",\"failed\":0}\n"
	for _, tc := range []struct {
		name, log string
		valid     bool
	}{
		{"both tiers with diagnostics", "diagnostic\n" + local + live + end, true},
		{"live header but no live case", local + "--- (live) ---\n" + end, false},
		{"missing local tier", live + end, false},
		{"empty run", end, false},
		{"unfinished run", local + live, false},
		{"failed case", local + strings.Replace(live, "passed", "failed", 1) + end, false},
		{"skipped case", local + strings.Replace(live, "passed", "skipped", 1) + end, false},
		{"unexpected manifest", local + strings.Replace(live, "live", "elsewhere", 1) + end, false},
		{"failed summary", local + live + strings.Replace(end, ":0", ":1", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, err := verify(strings.NewReader(tc.log), []string{"local", "live"})
			if (err == nil) != tc.valid {
				t.Fatalf("counts=%v error=%v", counts, err)
			}
			if tc.valid && (counts["local"] != 1 || counts["live"] != 1) {
				t.Fatalf("wrong counts: %v", counts)
			}
		})
	}
	if _, err := verify(strings.NewReader(local+live+end), nil); err == nil {
		t.Fatal("empty manifest requirement accepted")
	}
}
