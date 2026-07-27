package feedback

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
)

// Valid human verdicts on a review finding.
var validVerdicts = map[string]bool{
	"accurate":          true,
	"false_positive":    true,
	"noisy":             true,
	"overly_strict":     true,
	"partially_correct": true,
	"missed_context":    true,
}

var ErrReviewProjectionNotFound = errors.New("feedback: review projection not found")

// ReviewRef identifies a specific review session.
type ReviewRef struct {
	Repo     string `json:"repo"`
	Commit   string `json:"commit"`
	ReviewTS string `json:"review_timestamp,omitempty"`
}

// FeedbackRequest is the POST body for submitting feedback.
type FeedbackRequest struct {
	ReviewSnapshotID *snapshot.SnapshotID `json:"review_snapshot_id,omitempty"`
	ReviewRef        ReviewRef            `json:"review_ref"`
	FindingID        string               `json:"finding_id"`
	FindingType      string               `json:"finding_type"`
	FindingSnapshot  json.RawMessage      `json:"finding_snapshot"`
	Verdict          string               `json:"verdict"`
	Confidence       float64              `json:"confidence"`
	Notes            string               `json:"notes"`
	Reviewer         string               `json:"reviewer"`
	Source           string               `json:"source"`
}

func (r *FeedbackRequest) validate() error {
	if r.ReviewSnapshotID != nil {
		if err := r.ReviewSnapshotID.Validate(); err != nil {
			return fmt.Errorf("review_snapshot_id: %w", err)
		}
	} else {
		if r.ReviewRef.Repo == "" {
			return fmt.Errorf("review_ref.repo is required")
		}
		if r.ReviewRef.Commit == "" {
			return fmt.Errorf("review_ref.commit is required")
		}
	}
	if r.FindingID == "" {
		return fmt.Errorf("finding_id is required")
	}
	if strings.TrimSpace(r.Reviewer) == "" {
		return fmt.Errorf("reviewer is required")
	}
	if !validVerdicts[r.Verdict] {
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	return nil
}

// StoredFeedback is the persisted form of a feedback record.
type StoredFeedback struct {
	ReviewSnapshotID *snapshot.SnapshotID `json:"review_snapshot_id,omitempty"`
	ReviewTeamName   string               `json:"review_team_name,omitempty"`
	ReviewRef        ReviewRef            `json:"review_ref"`
	FindingID        string               `json:"finding_id"`
	FindingType      string               `json:"finding_type,omitempty"`
	FindingSnapshot  json.RawMessage      `json:"finding_snapshot,omitempty"`
	Verdict          string               `json:"verdict"`
	Confidence       float64              `json:"confidence"`
	Notes            string               `json:"notes,omitempty"`
	Reviewer         string               `json:"reviewer"`
	Source           string               `json:"source,omitempty"`
}

// Store is the interface for feedback persistence.
type Store interface {
	Save(rec *StoredFeedback) error
}

// SnapshotStore is the canonical projected-review read extension: feedback is
// keyed by the review snapshot, the only review identity the platform writes.
type SnapshotStore interface {
	GetByReviewSnapshot(snapshot.SnapshotID, string) ([]StoredFeedback, error)
}

// HandlerOption configures optional Handler behavior.
type HandlerOption func(*Handler)

// IdentityFunc returns the authenticated reviewer's stable name. Production
// wiring must supply this so caller-controlled JSON cannot impersonate or
// overwrite another reviewer's feedback.
type IdentityFunc func(*http.Request) string

func WithIdentity(fn IdentityFunc) HandlerOption {
	return func(h *Handler) { h.identity = fn }
}

func WithSnapshotTeam(teamName string) HandlerOption {
	return func(h *Handler) { h.snapshotTeam = teamName }
}

// Handler serves the agent feedback API.
type Handler struct {
	store        Store
	identity     IdentityFunc
	snapshotTeam string
}

// NewHandler creates a new feedback API handler.
func NewHandler(store Store, opts ...HandlerOption) *Handler {
	h := &Handler{store: store}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// SubmitFeedback handles POST /api/v1/agent/feedback.
func (h *Handler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	var req FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if h.identity != nil {
		req.Reviewer = strings.TrimSpace(h.identity(r))
		if req.Reviewer == "" {
			http.Error(w, "authenticated reviewer is unavailable", http.StatusForbidden)
			return
		}
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rec := &StoredFeedback{
		ReviewSnapshotID: req.ReviewSnapshotID,
		ReviewRef:        req.ReviewRef,
		FindingID:        req.FindingID,
		FindingType:      req.FindingType,
		FindingSnapshot:  req.FindingSnapshot,
		Verdict:          req.Verdict,
		Confidence:       req.Confidence,
		Notes:            req.Notes,
		Reviewer:         req.Reviewer,
		Source:           req.Source,
	}
	if req.ReviewSnapshotID != nil {
		rec.ReviewTeamName = h.snapshotTeam
	}

	if err := h.store.Save(rec); err != nil {
		if errors.Is(err, ErrReviewProjectionNotFound) {
			http.Error(w, "review projection not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// MemoryStore is an in-memory Store for testing.
type MemoryStore struct {
	mu      sync.Mutex
	records []*StoredFeedback
}

// NewMemoryStore creates an in-memory feedback store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Save(rec *StoredFeedback) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Projected feedback is authoritative by
	// (review_snapshot_id,finding_id,reviewer); legacy feedback retains the
	// repo/commit compatibility key.
	for i, existing := range m.records {
		sameReview := false
		if rec.ReviewSnapshotID != nil && existing.ReviewSnapshotID != nil {
			sameReview = *existing.ReviewSnapshotID == *rec.ReviewSnapshotID &&
				existing.ReviewTeamName == rec.ReviewTeamName
		} else if rec.ReviewSnapshotID == nil && existing.ReviewSnapshotID == nil {
			sameReview = existing.ReviewRef.Repo == rec.ReviewRef.Repo &&
				existing.ReviewRef.Commit == rec.ReviewRef.Commit
		}
		if sameReview && existing.FindingID == rec.FindingID && existing.Reviewer == rec.Reviewer {
			cp := cloneFeedback(rec)
			m.records[i] = &cp
			return nil
		}
	}
	cp := cloneFeedback(rec)
	m.records = append(m.records, &cp)
	return nil
}

func (m *MemoryStore) GetByReviewSnapshot(id snapshot.SnapshotID, teamName string) ([]StoredFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredFeedback{}
	for _, rec := range m.records {
		if rec.ReviewSnapshotID != nil && *rec.ReviewSnapshotID == id && rec.ReviewTeamName == teamName {
			results = append(results, cloneFeedback(rec))
		}
	}
	return results, nil
}

func cloneFeedback(rec *StoredFeedback) StoredFeedback {
	cp := *rec
	if rec.ReviewSnapshotID != nil {
		id := *rec.ReviewSnapshotID
		cp.ReviewSnapshotID = &id
	}
	cp.FindingSnapshot = append(json.RawMessage(nil), rec.FindingSnapshot...)
	return cp
}
