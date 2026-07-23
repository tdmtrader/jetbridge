package reviews

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/snapshot"
)

// BuildLookupFunc resolves a build ID to its context. found=false when
// the build does not exist.
type BuildLookupFunc func(buildID int) (BuildContext, bool, error)

// Handler serves the agent reviews API.
type Handler struct {
	store          Store
	feedbackStore  feedback.Store
	lookupBuild    BuildLookupFunc
	publishToken   string
	projectionTeam string
}

func NewHandler(store Store, feedbackStore feedback.Store, lookup BuildLookupFunc, publishToken, projectionTeam string) *Handler {
	return &Handler{
		store:          store,
		feedbackStore:  feedbackStore,
		lookupBuild:    lookup,
		publishToken:   publishToken,
		projectionTeam: projectionTeam,
	}
}

// Finding matches the shape stored in review JSON for proven issues
// and observations (superset; observations leave test fields empty).
type Finding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Category    string `json:"category"`
	TestFile    string `json:"test_file,omitempty"`
	TestName    string `json:"test_name,omitempty"`
	TestOutput  string `json:"test_output,omitempty"`
}

// FindingFeedback is the recorded verdict for one finding.
type FindingFeedback struct {
	Verdict  string `json:"verdict"`
	Notes    string `json:"notes,omitempty"`
	Reviewer string `json:"reviewer"`
}

// BuildReviewResponse is the GET /api/v1/builds/:build_id/agent-reviews
// element: summary fields plus unpacked findings and feedback.
type BuildReviewResponse struct {
	StoredReview
	ProvenIssues []Finding                  `json:"proven_issues"`
	Observations []Finding                  `json:"observations"`
	Feedback     map[string]FindingFeedback `json:"feedback"`
	FindingCount int                        `json:"finding_count"`
}

// SubmitReview handles POST /api/v1/agent/reviews.
//
// Auth: the wrappa wraps this route in principal(reviews:write) with a
// legacy bypass (atc/wrappa/api_auth_wrappa.go). A verified principal
// arrives via the request context; anything else falls back to the
// static publish token this handler has always validated (dual-accept
// window; removed with --agent-review-publish-token).
func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	submittedBy := ""
	principalTeam := ""
	if p, ok := principals.FromContext(r.Context()); ok {
		submittedBy = p.Name
		principalTeam = p.TeamName
	} else {
		if h.publishToken == "" {
			http.Error(w, "agent review publishing is not enabled", http.StatusForbidden)
			return
		}
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.publishToken)) != 1 {
			http.Error(w, "invalid publish token", http.StatusUnauthorized)
			return
		}
		submittedBy = principals.LegacyPublishPrincipalName
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body exceeds 4MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	sub, err := ParseSubmission(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	buildCtx, found, err := h.lookupBuild(sub.BuildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "build not found", http.StatusNotFound)
		return
	}
	if principalTeam != "" && principalTeam != buildCtx.TeamName {
		http.Error(w, "principal is not authorized for the build team", http.StatusForbidden)
		return
	}
	if submittedBy != principals.LegacyPublishPrincipalName && principalTeam == "" {
		http.Error(w, "principal team is required", http.StatusForbidden)
		return
	}

	rec := sub.ToStoredReview(buildCtx)
	rec.SubmittedBy = submittedBy
	if err := h.store.Upsert(rec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
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
// run. Repository and commit are presentation fields and do not collapse rows.
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
		var payload ReviewPayload
		if err := json.Unmarshal(rec.Review, &payload); err == nil {
			resp.ProvenIssues = decodeFindings(payload.ProvenIssues)
			resp.Observations = decodeFindings(payload.Observations)
			resp.FindingCount = len(resp.ProvenIssues) + len(resp.Observations)
		} else {
			resp.FindingCount = rec.ProvenCount + rec.ObservationCount
		}
		if resp.ProvenIssues == nil {
			resp.ProvenIssues = []Finding{}
		}
		if resp.Observations == nil {
			resp.Observations = []Finding{}
		}

		var fbs []feedback.StoredFeedback
		var err error
		if rec.SnapshotID != nil {
			if store, ok := h.feedbackStore.(feedback.SnapshotStore); ok {
				fbs, err = store.GetByReviewSnapshot(*rec.SnapshotID, rec.TeamName)
			} else {
				fbs, err = h.feedbackStore.GetByReview(rec.Repo, rec.CommitSha)
			}
		} else {
			fbs, err = h.feedbackStore.GetByReview(rec.Repo, rec.CommitSha)
		}
		if err == nil {
			for _, fb := range fbs {
				resp.Feedback[fb.FindingID] = FindingFeedback{
					Verdict: fb.Verdict, Notes: fb.Notes, Reviewer: fb.Reviewer,
				}
			}
		}
		resp.EvaluatedCount = len(resp.Feedback)
		resp.Review = nil
		responses = append(responses, resp)
	}
	return responses
}

func decodeFindings(raws []json.RawMessage) []Finding {
	findings := make([]Finding, 0, len(raws))
	for _, raw := range raws {
		var f Finding
		// tolerate partial decode — never drop a finding the ingest counted
		json.Unmarshal(raw, &f)
		findings = append(findings, f)
	}
	return findings
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
		Repo:     r.URL.Query().Get("repo"),
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
