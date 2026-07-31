package reviewgrade

// The produced-review shapes below intentionally duplicate the read-only
// fields of agent/snapshot/contracts.ReviewBody rather than importing the
// root module. bench/harness must stay independent of the product module.

// Review is a produced review/v1 record body.
type Review struct {
	Conclusion string    `json:"conclusion"`
	Summary    string    `json:"summary"`
	Findings   []Finding `json:"findings"`
}

// Finding is one defect the agent reported.
type Finding struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	Blocking       bool     `json:"blocking"`
	Category       string   `json:"category"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Evidence       []Anchor `json:"evidence"`
	Recommendation string   `json:"recommendation,omitempty"`
}

// Anchor binds a finding to a location in a subject.
type Anchor struct {
	Subject string  `json:"subject"`
	Locator Locator `json:"locator"`
}

// Locator is the anchored position within a subject.
type Locator struct {
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Start   *int   `json:"start,omitempty"`
	End     *int   `json:"end,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Value   string `json:"value,omitempty"`
}

// Matches reports whether produced is a location-plausible match for expected.
//
// This is deliberately a LOCATION test only. It answers "did the agent point
// at the right code?", never "did the agent say the right thing" — that
// judgment stays with a human or a judge, per each case's rubric.md. A match
// here is a CANDIDATE, and Report labels it as such.
//
// tolerance widens the accepted line window on both sides, absorbing the
// ordinary drift between an oracle's hand-written range and an agent's
// citation.
func Matches(expected ExpectedFinding, produced Finding, tolerance int) bool {
	if tolerance < 0 {
		tolerance = 0
	}
	for _, site := range expected.Sites() {
		for _, evidence := range produced.Evidence {
			if evidence.Locator.Path == "" || evidence.Locator.Path != site.File {
				continue
			}
			// The oracle gave no pinnable range: any hit in the file counts.
			if site.Start == 0 {
				return true
			}
			// The agent cited a file but no lines: credit it generously.
			if evidence.Locator.Start == nil || evidence.Locator.End == nil {
				return true
			}
			if overlaps(*evidence.Locator.Start, *evidence.Locator.End, site.Start-tolerance, site.End+tolerance) {
				return true
			}
		}
	}
	return false
}

func overlaps(aStart, aEnd, bStart, bEnd int) bool {
	if aEnd < aStart {
		aStart, aEnd = aEnd, aStart
	}
	return aStart <= bEnd && bStart <= aEnd
}
