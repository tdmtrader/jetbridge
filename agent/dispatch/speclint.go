package dispatch

import (
	"fmt"
	"regexp"
)

// Spec lint: pre-dispatch vocabulary warnings (ticket #46).
//
// Tickets #23/#24 were refused ON TURN 1 by the claude CLI's usage-policy
// check — a false positive triggered purely by the ticket prose
// ("flight recorder + evidence + advisory judge + harvest" pattern-matched
// an automated-judgment/surveillance category; see SUPERVISION.md,
// "Tickets #23-#25 — the refusal detour"). The reworded successor (#25:
// "run-report files + optional rubric scoring", CI context stated first)
// sailed through. Each false refusal burns a dispatch's admission work and
// real dollars, so SpecLint warns at queue/dispatch time.
//
// This is ADVISORY ONLY: it never blocks a dispatch, never mutates the
// spec, and its warnings ride additively on the dispatch response
// (warnings []string, omitempty) / fly stderr / the dispatcher's info log.
//
// MAINTAINERS: when a dispatch is diagnosed as a CLI usage-policy false
// refusal, generalize the offending phrasing into a new entry below
// (case-insensitive regex + one-line reason). Keep patterns conservative —
// a noisy linter gets ignored, and a warning here must never read as a
// verdict on the ticket's intent.
var specLintPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{
		re:     regexp.MustCompile(`(?i)flight[\s-]*recorder`),
		reason: `reads as covert session recording (the exact #23/#24 trigger); "run-report files" sailed through`,
	},
	{
		re:     regexp.MustCompile(`(?i)\bsurveillance\b`),
		reason: `names a surveillance category outright; describe the CI observability goal instead`,
	},
	{
		re:     regexp.MustCompile(`(?i)\b(?:advisory|automated)\s+judg(?:e|es|ing|ment)\b`),
		reason: `reads as automated judgment of people; state the CI context first (e.g. "optional rubric scoring of build output")`,
	},
	{
		re:     regexp.MustCompile(`(?i)\bevidence\s+(?:collection|gathering|capture|harvest\w*)\b`),
		reason: `evidence-collection phrasing reads as investigative tooling; name the concrete artifact (run-report files, build logs) instead`,
	},
}

// SpecLint scans a spec's title and body for vocabulary known to trigger
// claude CLI usage-policy false refusals and returns one advisory warning
// per matched table pattern (empty = clean). Purely informational: callers
// must never block, error, or mutate on its output.
func SpecLint(title, body string) []string {
	text := title + "\n" + body
	var warnings []string
	for _, p := range specLintPatterns {
		if match := p.re.FindString(text); match != "" {
			warnings = append(warnings, fmt.Sprintf("%q: %s", match, p.reason))
		}
	}
	return warnings
}
