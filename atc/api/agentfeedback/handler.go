package agentfeedback

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Valid verdicts matching the ci-agent schema.
var validVerdicts = map[string]bool{
	"accurate":           true,
	"false_positive":     true,
	"noisy":              true,
	"overly_strict":      true,
	"partially_correct":  true,
	"missed_context":     true,
}

// ReviewRef identifies a specific review session.
type ReviewRef struct {
	Repo     string `json:"repo"`
	Commit   string `json:"commit"`
	ReviewTS string `json:"review_timestamp,omitempty"`
}

// FeedbackRequest is the POST body for submitting feedback.
type FeedbackRequest struct {
	ReviewRef       ReviewRef       `json:"review_ref"`
	FindingID       string          `json:"finding_id"`
	FindingType     string          `json:"finding_type"`
	FindingSnapshot json.RawMessage `json:"finding_snapshot"`
	Verdict         string          `json:"verdict"`
	Confidence      float64         `json:"confidence"`
	Notes           string          `json:"notes"`
	Reviewer        string          `json:"reviewer"`
	Source          string          `json:"source"`
}

func (r *FeedbackRequest) validate() error {
	if r.ReviewRef.Repo == "" {
		return fmt.Errorf("review_ref.repo is required")
	}
	if r.ReviewRef.Commit == "" {
		return fmt.Errorf("review_ref.commit is required")
	}
	if r.FindingID == "" {
		return fmt.Errorf("finding_id is required")
	}
	if !validVerdicts[r.Verdict] {
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	return nil
}

// StoredFeedback is the persisted form of a feedback record.
type StoredFeedback struct {
	ReviewRef       ReviewRef       `json:"review_ref"`
	FindingID       string          `json:"finding_id"`
	FindingType     string          `json:"finding_type,omitempty"`
	FindingSnapshot json.RawMessage `json:"finding_snapshot,omitempty"`
	Verdict         string          `json:"verdict"`
	Confidence      float64         `json:"confidence"`
	Notes           string          `json:"notes,omitempty"`
	Reviewer        string          `json:"reviewer"`
	Source          string          `json:"source,omitempty"`
}

// SummaryResponse is the GET /summary response.
type SummaryResponse struct {
	Total        int            `json:"total"`
	AccuracyRate float64        `json:"accuracy_rate"`
	FPRate       float64        `json:"false_positive_rate"`
	ByVerdict    map[string]int `json:"by_verdict"`
}

// ClassifyRequest is the POST body for verdict classification.
type ClassifyRequest struct {
	Text string `json:"text"`
}

// ClassifyResponse is the response from verdict classification.
type ClassifyResponse struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
}

// Store is the interface for feedback persistence.
type Store interface {
	Save(rec *StoredFeedback) error
	GetByReview(repo, commit string) ([]StoredFeedback, error)
	GetAll() ([]StoredFeedback, error)
}

// Handler serves the agent feedback API.
type Handler struct {
	store Store
}

// NewHandler creates a new feedback API handler.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// SubmitFeedback handles POST /api/v1/agent/feedback.
func (h *Handler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	var req FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rec := &StoredFeedback{
		ReviewRef:       req.ReviewRef,
		FindingID:       req.FindingID,
		FindingType:     req.FindingType,
		FindingSnapshot: req.FindingSnapshot,
		Verdict:         req.Verdict,
		Confidence:      req.Confidence,
		Notes:           req.Notes,
		Reviewer:        req.Reviewer,
		Source:          req.Source,
	}

	if err := h.store.Save(rec); err != nil {
		http.Error(w, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// GetFeedback handles GET /api/v1/agent/feedback?repo=...&commit=...
func (h *Handler) GetFeedback(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	commit := r.URL.Query().Get("commit")

	results, err := h.store.GetByReview(repo, commit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = []StoredFeedback{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// GetSummary handles GET /api/v1/agent/feedback/summary.
func (h *Handler) GetSummary(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.GetAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	repo := r.URL.Query().Get("repo")

	summary := SummaryResponse{
		ByVerdict: make(map[string]int),
	}

	for _, rec := range all {
		if repo != "" && rec.ReviewRef.Repo != repo {
			continue
		}
		summary.Total++
		summary.ByVerdict[rec.Verdict]++
	}

	if summary.Total > 0 {
		summary.AccuracyRate = float64(summary.ByVerdict["accurate"]) / float64(summary.Total)
		summary.FPRate = float64(summary.ByVerdict["false_positive"]) / float64(summary.Total)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

// ClassifyVerdict handles POST /api/v1/agent/feedback/classify.
func (h *Handler) ClassifyVerdict(w http.ResponseWriter, r *http.Request) {
	var req ClassifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}

	verdict, confidence := classifyText(req.Text)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ClassifyResponse{
		Verdict:    verdict,
		Confidence: confidence,
	})
}

// classifyText is a simplified keyword-based classifier (mirrors ci-agent/feedback/classifier.go).
func classifyText(text string) (string, float64) {
	lower := strings.ToLower(text)

	// Negation check.
	if strings.Contains(lower, "not a false positive") || strings.Contains(lower, "not false positive") {
		return "accurate", 0.80
	}

	type kw struct {
		words   []string
		verdict string
		conf    float64
	}

	keywords := []kw{
		{[]string{"good catch", "real bug", "real issue", "correct", "accurate"}, "accurate", 0.85},
		{[]string{"false positive", "not a bug", "not an issue", "expected behavior"}, "false_positive", 0.85},
		{[]string{"noisy", "not important", "trivial", "low priority"}, "noisy", 0.80},
		{[]string{"style issue", "preference", "nitpick", "too strict"}, "overly_strict", 0.80},
		{[]string{"partially right", "partially correct", "wrong diagnosis"}, "partially_correct", 0.75},
		{[]string{"missing context", "lacks context", "needs more context"}, "missed_context", 0.75},
	}

	for _, k := range keywords {
		for _, w := range k.words {
			if strings.Contains(lower, w) {
				return k.verdict, k.conf
			}
		}
	}

	return "accurate", 0.3
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

	// Upsert: replace if same repo+commit+finding_id+reviewer.
	for i, existing := range m.records {
		if existing.ReviewRef.Repo == rec.ReviewRef.Repo &&
			existing.ReviewRef.Commit == rec.ReviewRef.Commit &&
			existing.FindingID == rec.FindingID &&
			existing.Reviewer == rec.Reviewer {
			m.records[i] = rec
			return nil
		}
	}
	m.records = append(m.records, rec)
	return nil
}

func (m *MemoryStore) GetByReview(repo, commit string) ([]StoredFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []StoredFeedback
	for _, rec := range m.records {
		if rec.ReviewRef.Repo == repo && rec.ReviewRef.Commit == commit {
			results = append(results, *rec)
		}
	}
	return results, nil
}

func (m *MemoryStore) GetAll() ([]StoredFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []StoredFeedback
	for _, rec := range m.records {
		results = append(results, *rec)
	}
	return results, nil
}
