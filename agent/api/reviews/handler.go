package reviews

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// Handler serves the agent reviews API.
type Handler struct {
	store          Store
	feedbackStore  feedback.Store
	projectionTeam string
}

func NewHandler(store Store, feedbackStore feedback.Store, projectionTeam string) *Handler {
	return &Handler{
		store:          store,
		feedbackStore:  feedbackStore,
		projectionTeam: projectionTeam,
	}
}

// Finding is one review/v1 finding on the wire. It carries what the record
// carries: identity, severity, whether the finding blocks, its prose, and the
// first file-anchored evidence locator. The legacy payload's test_file /
// test_name / test_output have no counterpart in review/v1 and are gone rather
// than emitted empty.
type Finding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Blocking    bool   `json:"blocking"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Category    string `json:"category"`
}

// FindingFeedback is the recorded verdict for one finding.
type FindingFeedback struct {
	Verdict  string `json:"verdict"`
	Notes    string `json:"notes,omitempty"`
	Reviewer string `json:"reviewer"`
}

// BuildReviewResponse is one review rendered from its sealed record: the
// projection's summary fields plus the record's findings, split on severity,
// and the reader's own verdicts.
type BuildReviewResponse struct {
	StoredReview
	ProvenIssues []Finding                  `json:"proven_issues"`
	Observations []Finding                  `json:"observations"`
	Feedback     map[string]FindingFeedback `json:"feedback"`
	FindingCount int                        `json:"finding_count"`
}

// GetByBuild handles GET /api/v1/builds/:build_id/agent-reviews.
func (h *Handler) GetByBuild(w http.ResponseWriter, r *http.Request) {
	buildID, err := strconv.Atoi(r.FormValue(":build_id"))
	if err != nil {
		http.Error(w, "invalid build_id", http.StatusBadRequest)
		return
	}

	recs, err := h.store.GetByBuild(buildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	responses := h.detailResponses(recs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

// GetBySnapshot returns the projection of one canonical review/v1 snapshot.
// The snapshot ID, not repository/commit, is the identity boundary.
func (h *Handler) GetBySnapshot(w http.ResponseWriter, r *http.Request) {
	rawID, ok := exactRouteValue(r, "snapshot_id")
	if !ok {
		http.Error(w, "invalid snapshot_id", http.StatusBadRequest)
		return
	}
	id, err := snapshot.ParseSnapshotID(rawID)
	if err != nil {
		http.Error(w, "invalid snapshot_id", http.StatusBadRequest)
		return
	}
	store, ok := h.store.(ProjectionReader)
	if !ok {
		http.Error(w, "snapshot review projection is unavailable", http.StatusInternalServerError)
		return
	}
	rec, found, err := store.GetBySnapshot(h.projectionTeam, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "review projection not found", http.StatusNotFound)
		return
	}
	responses := h.detailResponses([]StoredReview{rec})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses[0])
}

// ListByWorkflowRun returns every review snapshot linked to a durable generic
// run, one element per production occurrence in the run.
func (h *Handler) ListByWorkflowRun(w http.ResponseWriter, r *http.Request) {
	workflowName, ok := exactRouteValue(r, "workflow_name")
	if !ok {
		http.Error(w, "invalid workflow_name", http.StatusBadRequest)
		return
	}
	rawID, ok := exactRouteValue(r, "workflow_run_id")
	if !ok {
		http.Error(w, "invalid workflow_run_id", http.StatusBadRequest)
		return
	}
	id, err := snapshot.ParseWorkflowRunID(rawID)
	if err != nil {
		http.Error(w, "invalid workflow_run_id", http.StatusBadRequest)
		return
	}
	store, ok := h.store.(ProjectionReader)
	if !ok {
		http.Error(w, "workflow review projection is unavailable", http.StatusInternalServerError)
		return
	}
	recs, err := store.ListByWorkflowRun(h.projectionTeam, workflowName, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responses := h.detailResponses(recs)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

// rata/pat encodes path parameters as reserved query keys. Require exactly
// one router-authored value so a caller cannot shadow durable snapshot/run
// identity with its own query parameter.
func exactRouteValue(r *http.Request, name string) (string, bool) {
	values := r.URL.Query()[":"+name]
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func (h *Handler) detailResponses(recs []StoredReview) []BuildReviewResponse {
	responses := make([]BuildReviewResponse, 0, len(recs))
	for _, rec := range recs {
		resp := BuildReviewResponse{StoredReview: rec, Feedback: map[string]FindingFeedback{}}
		resp.ProvenIssues, resp.Observations, resp.FindingCount = unpackFindings(rec)
		if resp.ProvenIssues == nil {
			resp.ProvenIssues = []Finding{}
		}
		if resp.Observations == nil {
			resp.Observations = []Finding{}
		}

		// Feedback is keyed by the review snapshot — the only review identity
		// the platform writes.
		if store, ok := h.feedbackStore.(feedback.SnapshotStore); ok {
			fbs, err := store.GetByReviewSnapshot(rec.SnapshotID, rec.TeamName)
			if err == nil {
				for _, fb := range fbs {
					resp.Feedback[fb.FindingID] = FindingFeedback{
						Verdict: fb.Verdict, Notes: fb.Notes, Reviewer: fb.Reviewer,
					}
				}
			}
		}
		resp.EvaluatedCount = len(resp.Feedback)
		resp.Review = nil
		responses = append(responses, resp)
	}
	return responses
}

// unpackFindings turns one stored review's bytes into the API's
// proven_issues/observations/finding_count triple.
//
// There is one shape to decode. Every stored review holds the sealed review/v1
// record.json that UpsertReviewProjection wrote, so this read goes through the
// same READ-TIME gate the projector used: the declared core schema first, then
// review/v1's semantic rules. A record this rejects is not downgraded to some
// looser decode — the projected severity counts stay the only claim.
func unpackFindings(rec StoredReview) ([]Finding, []Finding, int) {
	record, err := contracts.DecodeSealedReviewRecord(rec.Review)
	if err != nil {
		return nil, nil, rec.FindingCount()
	}
	var proven, observations []Finding
	for _, finding := range record.Body.Findings {
		if finding.Severity == observationSeverity {
			observations = append(observations, recordFinding(finding))
			continue
		}
		proven = append(proven, recordFinding(finding))
	}
	return proven, observations, len(record.Body.Findings)
}

// observationSeverity is review/v1's own name for a finding that asserts
// nothing actionable. The contract makes it the exact complement of a
// substantive finding: an observation may never be blocking, and high and
// critical must be, so splitting on severity == "observation" is the same cut
// the projector's severity counts already record, and the two cannot disagree
// about where a blocking finding lands.
const observationSeverity = "observation"

// recordFinding maps one review/v1 finding onto the wire shape the web decodes.
// Recommendation has no field in that shape, so it stays out rather than being
// folded into the description.
func recordFinding(finding contracts.Finding) Finding {
	mapped := Finding{
		ID: finding.ID, Severity: finding.Severity, Blocking: finding.Blocking,
		Title: finding.Title, Description: finding.Description, Category: finding.Category,
	}
	mapped.File, mapped.Line = findingLocation(finding.Evidence)
	return mapped
}

// findingLocation takes the first file-anchored evidence locator. Log lines,
// JSON pointers, byte ranges and opaque anchors name no source file, so they
// leave file/line empty instead of pointing the reader at a path that is not
// one.
func findingLocation(evidence []contracts.Anchor) (string, int) {
	for _, anchor := range evidence {
		if anchor.Locator.Kind != "file-lines" || anchor.Locator.Start == nil {
			continue
		}
		return anchor.Locator.Path, *anchor.Locator.Start
	}
	return "", 0
}

// ListByTeam handles GET /api/v1/teams/:team_name/agent-reviews.
func (h *Handler) ListByTeam(w http.ResponseWriter, r *http.Request) {
	team := r.FormValue(":team_name")
	if team == "" {
		http.Error(w, "team_name is required", http.StatusBadRequest)
		return
	}

	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}

	recs, err := h.store.ListByTeam(team, ListFilter{
		Pipeline: r.URL.Query().Get("pipeline"),
		Limit:    limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range recs {
		recs[i].Review = nil
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}
