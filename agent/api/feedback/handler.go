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

// Valid verdicts matching the ci-agent schema.
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

// SnapshotStore is the canonical projected-review extension. Legacy stores
// need only implement Store; handlers fall back to repo/commit reads for old
// build reviews when this extension is absent.
type SnapshotStore interface {
	GetByReviewSnapshot(snapshot.SnapshotID, string) ([]StoredFeedback, error)
}

// ClassifyFunc is a function that classifies natural language text into a verdict.
type ClassifyFunc func(text string) (verdict string, confidence float64)

// HandlerOption configures optional Handler behavior.
type HandlerOption func(*Handler)

// IdentityFunc returns the authenticated reviewer's stable name. Production
// wiring must supply this so caller-controlled JSON cannot impersonate or
// overwrite another reviewer's feedback.
type IdentityFunc func(*http.Request) string

// WithClassifier sets a custom classification function.
func WithClassifier(fn ClassifyFunc) HandlerOption {
	return func(h *Handler) { h.classifier = fn }
}

func WithIdentity(fn IdentityFunc) HandlerOption {
	return func(h *Handler) { h.identity = fn }
}

func WithSnapshotTeam(teamName string) HandlerOption {
	return func(h *Handler) { h.snapshotTeam = teamName }
}

// Handler serves the agent feedback API.
type Handler struct {
	store        Store
	classifier   ClassifyFunc
	identity     IdentityFunc
	snapshotTeam string
}

// NewHandler creates a new feedback API handler.
func NewHandler(store Store, opts ...HandlerOption) *Handler {
	h := &Handler{store: store, classifier: defaultClassifyText}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Finding represents a review finding matching the Elm frontend's Finding type.
type Finding struct {
	ID          string `json:"id"`
	FindingType string `json:"finding_type"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	TestCode    string `json:"test_code"`
}

// GetFindings handles GET /api/v1/agent/reviews/:commit/findings.
func (h *Handler) GetFindings(w http.ResponseWriter, r *http.Request) {
	commit := r.FormValue(":commit")
	if commit == "" {
		http.Error(w, "commit is required", http.StatusBadRequest)
		return
	}

	records, err := h.store.GetByReview("", commit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Extract findings from stored feedback snapshots, deduplicate by FindingID.
	seen := make(map[string]bool)
	var findings []Finding
	for _, rec := range records {
		if rec.FindingID == "" || seen[rec.FindingID] {
			continue
		}
		seen[rec.FindingID] = true

		var f Finding
		if len(rec.FindingSnapshot) > 0 {
			json.Unmarshal(rec.FindingSnapshot, &f)
		}
		// Ensure the ID matches the record's finding_id.
		f.ID = rec.FindingID
		if f.FindingType == "" {
			f.FindingType = rec.FindingType
		}
		findings = append(findings, f)
	}

	if findings == nil {
		findings = []Finding{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(findings)
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

// GetFeedback handles GET /api/v1/agent/feedback?repo=...&commit=...
func (h *Handler) GetFeedback(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("review_snapshot_id"); raw != "" {
		id, err := snapshot.ParseSnapshotID(raw)
		if err != nil {
			http.Error(w, "invalid review_snapshot_id", http.StatusBadRequest)
			return
		}
		store, ok := h.store.(SnapshotStore)
		if !ok {
			http.Error(w, "snapshot feedback is unavailable", http.StatusInternalServerError)
			return
		}
		results, err := store.GetByReviewSnapshot(id, h.snapshotTeam)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if results == nil {
			results = []StoredFeedback{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
		return
	}
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

	verdict, confidence := h.classifier(req.Text)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ClassifyResponse{
		Verdict:    verdict,
		Confidence: confidence,
	})
}

// negationPrefixes are phrases that invert a "false positive" match.
var negationPrefixes = []string{"not a ", "not ", "isn't a ", "isn't ", "no "}

type classifierPattern struct {
	keywords   []string
	verdict    string
	confidence float64
}

// classifierPatterns mirrors ci-agent/feedback/classifier.go patterns exactly.
var classifierPatterns = []classifierPattern{
	{keywords: []string{"good catch", "real bug", "real issue", "correct", "accurate", "valid finding", "agree"}, verdict: "accurate", confidence: 0.85},
	{keywords: []string{"false positive", "not a bug", "not an issue", "doesn't apply", "expected behavior", "by design", "intended"}, verdict: "false_positive", confidence: 0.85},
	{keywords: []string{"noisy", "not important", "too minor", "trivial", "low priority", "don't care", "not worth"}, verdict: "noisy", confidence: 0.80},
	{keywords: []string{"style issue", "preference", "opinionated", "subjective", "nitpick", "too strict", "overly strict"}, verdict: "overly_strict", confidence: 0.80},
	{keywords: []string{"partially right", "partially correct", "right area", "wrong diagnosis", "close but", "half right"}, verdict: "partially_correct", confidence: 0.75},
	{keywords: []string{"missing context", "lacks context", "needs more context", "doesn't know", "not aware", "can't tell"}, verdict: "missed_context", confidence: 0.75},
}

// defaultClassifyText is the keyword-based classifier matching ci-agent/feedback/classifier.go.
func defaultClassifyText(text string) (string, float64) {
	lower := strings.ToLower(text)

	// Check for negation patterns that flip the verdict.
	for _, prefix := range negationPrefixes {
		if strings.Contains(lower, prefix+"false positive") {
			return "accurate", 0.80
		}
	}

	// Match against patterns using best-confidence-wins strategy.
	var bestVerdict string
	var bestConfidence float64

	for _, p := range classifierPatterns {
		for _, kw := range p.keywords {
			if strings.Contains(lower, kw) {
				if p.confidence > bestConfidence {
					bestVerdict = p.verdict
					bestConfidence = p.confidence
				}
			}
		}
	}

	if bestVerdict != "" {
		return bestVerdict, bestConfidence
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

func (m *MemoryStore) GetByReview(repo, commit string) ([]StoredFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []StoredFeedback
	for _, rec := range m.records {
		repoMatch := repo == "" || rec.ReviewRef.Repo == repo
		commitMatch := commit == "" || rec.ReviewRef.Commit == commit
		if repoMatch && commitMatch {
			results = append(results, *rec)
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

func (m *MemoryStore) GetAll() ([]StoredFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []StoredFeedback
	for _, rec := range m.records {
		results = append(results, *rec)
	}
	return results, nil
}
