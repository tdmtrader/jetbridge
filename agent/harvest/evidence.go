package harvest

// Evidence is the review.json payload (§6.4.1): the existing
// ReviewPayload shape plus additive gates/judge/judge_error keys —
// consumers (atc reviews ingestion, the Elm review embed) ignore
// unknown keys and only count proven_issues/observations.
type Evidence struct {
	SchemaVersion string           `json:"schema_version"` // "harvest/1"
	Metadata      EvidenceMetadata `json:"metadata"`
	Score         EvidenceScore    `json:"score"`
	ProvenIssues  []EvidenceIssue  `json:"proven_issues"`
	Observations  []EvidenceIssue  `json:"observations"`
	Summary       string           `json:"summary"`
	Gates         []GateOutcome    `json:"gates"`
	Judge         *EvidenceJudge   `json:"judge,omitempty"`
	JudgeError    string           `json:"judge_error,omitempty"`
}

type EvidenceMetadata struct {
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Branch      string `json:"branch"`
	AgentModel  string `json:"agent_model"`
	DurationSec int    `json:"duration_seconds"`
}

type EvidenceScore struct {
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Pass  bool    `json:"pass"`
}

// EvidenceIssue matches the findings shape the reviews consumers parse
// (id/severity/title/description/file/line/category).
type EvidenceIssue struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Category    string `json:"category"`
}

type EvidenceJudge struct {
	RubricHash     string              `json:"rubric_hash"`
	Dimensions     []EvidenceDimension `json:"dimensions"`
	Total          float64             `json:"total"`
	MaxTotal       float64             `json:"max_total"`
	Pass           bool                `json:"pass"`
	Model          string              `json:"model,omitempty"`
	CostUSD        float64             `json:"cost_usd,omitempty"`
	BudgetExceeded bool                `json:"budget_exceeded,omitempty"`
}

type EvidenceDimension struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale,omitempty"`
}
