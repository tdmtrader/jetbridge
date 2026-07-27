package dispatch_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/dispatch"
)

func TestWorkItemTextLintHitsKnownRefusalVocabulary(t *testing.T) {
	cases := []struct {
		name        string
		title, body string
		want        string // substring every warning for the case must carry
	}{
		{
			name: "flight recorder in body",
			body: "Wire the flight recorder into the runner.",
			want: "flight recorder",
		},
		{
			name:  "flight-recorder hyphenated in title",
			title: "runner flight-recorder T8-9",
			want:  "flight-recorder",
		},
		{
			name: "surveillance",
			body: "add surveillance of agent sessions",
			want: "surveillance",
		},
		{
			name: "advisory judge",
			body: "render the advisory judge onto the harvest step",
			want: "advisory judge",
		},
		{
			name: "evidence collection",
			body: "the step performs evidence collection for the judge",
			want: "evidence collection",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := dispatch.WorkItemTextLint(tc.title, tc.body)
			if len(warnings) == 0 {
				t.Fatalf("WorkItemTextLint(%q, %q) = no warnings, want a hit", tc.title, tc.body)
			}
			found := false
			for _, w := range warnings {
				if strings.Contains(strings.ToLower(w), strings.ToLower(tc.want)) {
					found = true
				}
			}
			if !found {
				t.Errorf("warnings %v must name the matched phrase %q", warnings, tc.want)
			}
		})
	}
}

func TestWorkItemTextLintIsCaseInsensitive(t *testing.T) {
	lower := dispatch.WorkItemTextLint("", "the flight recorder slice")
	upper := dispatch.WorkItemTextLint("", "the FLIGHT RECORDER slice")
	if len(lower) == 0 || len(upper) == 0 {
		t.Fatalf("case variants must both warn: lower=%v upper=%v", lower, upper)
	}
	if len(lower) != len(upper) {
		t.Errorf("case variants must warn identically often: lower=%v upper=%v", lower, upper)
	}
}

func TestWorkItemTextLintMissesBenignProse(t *testing.T) {
	warnings := dispatch.WorkItemTextLint(
		"run-report files + optional rubric scoring",
		"CI context: persist run-report files from the build and score them against the rubric.",
	)
	if len(warnings) != 0 {
		t.Errorf("the reworded #25 phrasing must lint clean, got %v", warnings)
	}
	if got := dispatch.WorkItemTextLint("", ""); len(got) != 0 {
		t.Errorf("empty spec must lint clean, got %v", got)
	}
}

func TestWorkItemTextLintOneWarningPerPattern(t *testing.T) {
	warnings := dispatch.WorkItemTextLint("flight recorder", "flight recorder everywhere: flight recorder")
	if len(warnings) != 1 {
		t.Errorf("repeated phrase must warn once per pattern, got %v", warnings)
	}
}
