package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const cursorVersion = 1

type pull struct {
	Number         int64    `json:"number"`
	HTMLURL        string   `json:"html_url"`
	State          string   `json:"state"`
	Merged         bool     `json:"merged"`
	Mergeable      *bool    `json:"mergeable"`
	MergeableState string   `json:"mergeable_state"`
	Head           pullHead `json:"head"`
	Base           pullBase `json:"base"`
}
type pullHead struct {
	Ref        string      `json:"ref"`
	SHA        string      `json:"sha"`
	Repository *repository `json:"repo"`
}
type pullBase struct {
	Ref        string      `json:"ref"`
	SHA        string      `json:"sha"`
	Repository *repository `json:"repo"`
}
type repository struct {
	FullName string `json:"full_name"`
}
type review struct {
	ID          int64      `json:"id"`
	User        ghUser     `json:"user"`
	Body        string     `json:"body"`
	CommitID    string     `json:"commit_id"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submitted_at"`
}
type ghUser struct {
	ID int64 `json:"id"`
}
type reviewComment struct {
	ID               int64  `json:"id"`
	ReviewID         *int64 `json:"pull_request_review_id"`
	User             ghUser `json:"user"`
	Body             string `json:"body"`
	CommitID         string `json:"commit_id"`
	OriginalCommitID string `json:"original_commit_id"`
	Path             string `json:"path"`
	Line             *int   `json:"line"`
	OriginalLine     *int   `json:"original_line"`
	InReplyToID      *int64 `json:"in_reply_to_id"`
}

type githubCursor struct {
	Version        int             `json:"v"`
	Watermark      reviewWatermark `json:"w"`
	BatchDigest    string          `json:"b"`
	StateSignature string          `json:"s"`
}
type reviewWatermark struct {
	SubmittedAt string `json:"a,omitempty"`
	ID          int64  `json:"i,omitempty"`
}

func (observer *Observer) Observe(ctx context.Context, locator pullrequest.Locator, rawCursor pullrequest.Cursor) (pullrequest.Observation, error) {
	if err := validateLocator(locator); err != nil {
		return pullrequest.Observation{}, err
	}
	cursor, err := decodeCursor(rawCursor)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	number, _ := strconv.ParseInt(locator.ExternalID, 10, 64)
	prURL := observer.endpoint("/repos/" + locator.Repository + "/pulls/" + locator.ExternalID)
	var providerPull pull
	if err := observer.get(ctx, prURL, &providerPull); err != nil {
		return pullrequest.Observation{}, err
	}
	if err := observer.validatePull(locator, number, providerPull); err != nil {
		return pullrequest.Observation{}, err
	}
	sourceRef, err := normalizeBranchRef(providerPull.Head.Ref)
	if err != nil {
		return pullrequest.Observation{}, fmt.Errorf("github source ref is invalid")
	}
	targetRef, err := normalizeBranchRef(providerPull.Base.Ref)
	if err != nil {
		return pullrequest.Observation{}, fmt.Errorf("github target ref is invalid")
	}
	state, err := normalizeState(providerPull)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	mergeability := normalizeMergeability(providerPull, state)
	observation := pullrequest.Observation{Locator: locator, URL: providerPull.HTMLURL, State: state, Mergeability: mergeability, SourceRef: sourceRef, SourceSHA: providerPull.Head.SHA, TargetRef: targetRef, TargetSHA: providerPull.Base.SHA, Iteration: strconv.FormatInt(providerPull.Number, 10)}
	if state == contracts.PullRequestActive {
		reviews, err := observer.reviews(ctx, locator)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		comments, err := observer.comments(ctx, locator)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		selected := selectReview(reviews, cursor.Watermark)
		if selected != nil {
			batch, threads, err := normalizeReview(*selected, comments)
			if err != nil {
				return pullrequest.Observation{}, err
			}
			observation.ReviewBatches = []pullrequest.ReviewBatch{batch}
			observation.Threads = threads
			cursor.Watermark = watermarkFor(*selected)
			cursor.BatchDigest = digestBatch(batch, threads)
		}
	}
	cursor.Version = cursorVersion
	cursor.StateSignature = digestState(observation)
	encoded, err := encodeCursor(cursor)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	observation.Cursor = pullrequest.Cursor(encoded)
	if err := observation.Validate(); err != nil {
		return pullrequest.Observation{}, fmt.Errorf("github normalized observation is invalid: %w", err)
	}
	return observation, nil
}

func validateLocator(locator pullrequest.Locator) error {
	if locator.Provider != pullrequest.ProviderGitHub {
		return fmt.Errorf("github observer requires a github locator")
	}
	parts := strings.Split(locator.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(locator.Repository, "\x00\r\n ") {
		return fmt.Errorf("github repository must be canonical owner/repository")
	}
	if locator.ExternalID == "" {
		return fmt.Errorf("github pull request number is required")
	}
	number, err := strconv.ParseInt(locator.ExternalID, 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != locator.ExternalID {
		return fmt.Errorf("github pull request number must be a canonical positive integer")
	}
	return nil
}

func (observer *Observer) validatePull(locator pullrequest.Locator, expectedNumber int64, value pull) error {
	if value.Number != expectedNumber || value.Head.Repository == nil || value.Head.Repository.FullName != locator.Repository || value.Base.Repository == nil || value.Base.Repository.FullName != locator.Repository {
		return fmt.Errorf("github pull request does not match its requested repository or number")
	}
	if err := validateObjectID(value.Head.SHA); err != nil {
		return fmt.Errorf("github source head is invalid")
	}
	if err := validateObjectID(value.Base.SHA); err != nil {
		return fmt.Errorf("github target head is invalid")
	}
	if strings.TrimSpace(value.Head.Ref) == "" || strings.TrimSpace(value.Base.Ref) == "" {
		return fmt.Errorf("github pull request refs are invalid")
	}
	if err := observer.validateHTMLURL(locator, expectedNumber, value.HTMLURL); err != nil {
		return err
	}
	return nil
}

func (observer *Observer) validateHTMLURL(locator pullrequest.Locator, number int64, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("github pull request URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("github pull request URL must not carry credentials, query, or fragment")
	}
	wantAuthority := observer.baseURL.Host
	if strings.EqualFold(observer.baseURL.Hostname(), "api.github.com") {
		wantAuthority = "github.com"
	}
	if !strings.EqualFold(parsed.Host, wantAuthority) {
		return fmt.Errorf("github pull request URL host does not match configured forge")
	}
	wantPath := "/" + locator.Repository + "/pull/" + strconv.FormatInt(number, 10)
	if parsed.Path != wantPath {
		return fmt.Errorf("github pull request URL path does not match requested pull request")
	}
	return nil
}

func normalizeState(value pull) (contracts.PullRequestState, error) {
	switch value.State {
	case "open":
		if value.Merged {
			return "", fmt.Errorf("github open pull request cannot be merged")
		}
		return contracts.PullRequestActive, nil
	case "closed":
		if value.Merged {
			return contracts.PullRequestCompleted, nil
		}
		return contracts.PullRequestAbandoned, nil
	default:
		return "", fmt.Errorf("github pull request state is unknown")
	}
}

func normalizeBranchRef(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.HasSuffix(raw, "/") || strings.Contains(raw, "//") || strings.Contains(raw, "..") || strings.Contains(raw, "@{") || strings.ContainsAny(raw, "\\~^:?*[ \t\r\n\x00") || strings.HasSuffix(raw, ".") {
		return "", fmt.Errorf("unsafe branch ref")
	}
	for _, component := range strings.Split(raw, "/") {
		if component == "" || component == "." || component == "@" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return "", fmt.Errorf("unsafe branch ref")
		}
	}
	return "refs/heads/" + raw, nil
}

func normalizeMergeability(value pull, state contracts.PullRequestState) contracts.PullRequestMergeability {
	if value.Mergeable == nil {
		return contracts.PullRequestUnknown
	}
	if !*value.Mergeable && value.MergeableState == "dirty" {
		return contracts.PullRequestConflicted
	}
	if *value.Mergeable {
		switch value.MergeableState {
		case "clean", "behind", "has_hooks":
			return contracts.PullRequestMergeable
		case "blocked", "draft", "unstable":
			return contracts.PullRequestPolicyBlocked
		}
	}
	return contracts.PullRequestUnknown
}

func (observer *Observer) reviews(ctx context.Context, locator pullrequest.Locator) ([]review, error) {
	target := observer.endpoint("/repos/" + locator.Repository + "/pulls/" + locator.ExternalID + "/reviews")
	query := target.Query()
	query.Set("per_page", "100")
	target.RawQuery = query.Encode()
	var output []review
	for pages, seen := 0, map[string]struct{}{}; target != nil; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("github reviews exceed pagination limit")
		}
		if _, duplicate := seen[target.String()]; duplicate {
			return nil, fmt.Errorf("github reviews pagination repeats a page")
		}
		seen[target.String()] = struct{}{}
		var page []review
		next, err := observer.getPage(ctx, target, &page)
		if err != nil {
			return nil, err
		}
		if len(output)+len(page) > 1024 {
			return nil, fmt.Errorf("github reviews exceed collection limit")
		}
		output = append(output, page...)
		target = next
	}
	return output, nil
}

func (observer *Observer) comments(ctx context.Context, locator pullrequest.Locator) ([]reviewComment, error) {
	target := observer.endpoint("/repos/" + locator.Repository + "/pulls/" + locator.ExternalID + "/comments")
	query := target.Query()
	query.Set("per_page", "100")
	target.RawQuery = query.Encode()
	var output []reviewComment
	for pages, seen := 0, map[string]struct{}{}; target != nil; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("github review comments exceed pagination limit")
		}
		if _, duplicate := seen[target.String()]; duplicate {
			return nil, fmt.Errorf("github review comments pagination repeats a page")
		}
		seen[target.String()] = struct{}{}
		var page []reviewComment
		next, err := observer.getPage(ctx, target, &page)
		if err != nil {
			return nil, err
		}
		if len(output)+len(page) > 512 {
			return nil, fmt.Errorf("github review comments exceed collection limit")
		}
		output = append(output, page...)
		target = next
	}
	return output, nil
}

func selectReview(reviews []review, watermark reviewWatermark) *review {
	completed := make([]review, 0, len(reviews))
	for _, review := range reviews {
		if review.SubmittedAt != nil {
			completed = append(completed, review)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if completed[i].SubmittedAt.Equal(*completed[j].SubmittedAt) {
			return completed[i].ID < completed[j].ID
		}
		return completed[i].SubmittedAt.Before(*completed[j].SubmittedAt)
	})
	for index := range completed {
		if afterWatermark(completed[index], watermark) {
			return &completed[index]
		}
	}
	return nil
}

func afterWatermark(value review, watermark reviewWatermark) bool {
	if watermark.ID == 0 {
		return true
	}
	instant, err := time.Parse(time.RFC3339Nano, watermark.SubmittedAt)
	if err != nil {
		return false
	}
	if value.SubmittedAt.After(instant) {
		return true
	}
	return value.SubmittedAt.Equal(instant) && value.ID > watermark.ID
}

func watermarkFor(value review) reviewWatermark {
	return reviewWatermark{SubmittedAt: value.SubmittedAt.UTC().Format(time.RFC3339Nano), ID: value.ID}
}

func normalizeReview(value review, all []reviewComment) (pullrequest.ReviewBatch, []pullrequest.Thread, error) {
	if value.ID <= 0 || value.User.ID <= 0 || strings.TrimSpace(value.CommitID) == "" || value.SubmittedAt == nil {
		return pullrequest.ReviewBatch{}, nil, fmt.Errorf("github submitted review is invalid")
	}
	if err := validateObjectID(value.CommitID); err != nil {
		return pullrequest.ReviewBatch{}, nil, fmt.Errorf("github submitted review commit is invalid")
	}
	threads, authority, err := normalizeThreads(value, all)
	if err != nil {
		return pullrequest.ReviewBatch{}, nil, err
	}
	if strings.TrimSpace(value.Body) != "" {
		threads = append(threads, pullrequest.Thread{ID: "review-" + strconv.FormatInt(value.ID, 10) + "-body", Iteration: strconv.FormatInt(value.ID, 10), Comments: []contracts.PullRequestComment{{ID: "review-" + strconv.FormatInt(value.ID, 10) + "-body-comment", Author: githubUser(value.User), Body: value.Body, CommitSHA: value.CommitID}}})
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
	sort.Strings(authority)
	batch := pullrequest.ReviewBatch{ID: "review-" + strconv.FormatInt(value.ID, 10), ReviewID: strconv.FormatInt(value.ID, 10), CommitSHA: value.CommitID, Reviewer: githubUser(value.User), Ready: true, ThreadIDs: authority}
	return batch, threads, nil
}

// normalizeThreads resolves reply chains against EVERY comment on the pull
// request, not just those the selected review submitted, and then emits only
// the threads that review touches.
//
// GitHub files a reply under the review that submitted it while in_reply_to_id
// still names a root belonging to an earlier review, so an index built from one
// review's comments cannot resolve it. That is not an edge case: the platform's
// own reply creates exactly this shape, which used to brick the observer
// permanently on the following poll.
func normalizeThreads(value review, all []reviewComment) ([]pullrequest.Thread, []string, error) {
	byID := make(map[int64]reviewComment, len(all))
	for _, comment := range all {
		if comment.ID <= 0 || comment.User.ID <= 0 || strings.TrimSpace(comment.Body) == "" {
			return nil, nil, fmt.Errorf("github review comment is invalid")
		}
		if _, exists := byID[comment.ID]; exists {
			return nil, nil, fmt.Errorf("github review comment id is duplicate")
		}
		byID[comment.ID] = comment
	}
	groups := make(map[int64][]reviewComment)
	for _, original := range all {
		current, root := original, original.ID
		seen := map[int64]struct{}{original.ID: struct{}{}}
		for current.InReplyToID != nil {
			parent, found := byID[*current.InReplyToID]
			if !found {
				return nil, nil, fmt.Errorf("github review reply has no root")
			}
			if _, duplicate := seen[parent.ID]; duplicate {
				return nil, nil, fmt.Errorf("github review reply cycle")
			}
			seen[parent.ID] = struct{}{}
			root = parent.ID
			current = parent
		}
		groups[root] = append(groups[root], original)
	}
	// Chains resolve against every comment, but this review only speaks for the
	// threads it actually contributed to. A thread it never touched belongs to
	// another batch and must not be claimed by this one.
	roots := make([]int64, 0, len(groups))
	for root, group := range groups {
		touched := false
		for _, comment := range group {
			if comment.ReviewID != nil && *comment.ReviewID == value.ID {
				touched = true
				break
			}
		}
		if touched {
			roots = append(roots, root)
		}
	}
	threads := make([]pullrequest.Thread, 0, len(roots))
	for _, rootID := range roots {
		root := byID[rootID]
		group := groups[rootID]
		sort.Slice(group, func(i, j int) bool { return commentID(group[i].ID) < commentID(group[j].ID) })
		thread := pullrequest.Thread{ID: "thread-" + strconv.FormatInt(rootID, 10), Iteration: strconv.FormatInt(value.ID, 10)}
		anchor, err := anchorFor(root)
		if err != nil {
			return nil, nil, err
		}
		thread.Anchor = anchor
		for _, comment := range group {
			commit := comment.OriginalCommitID
			if commit == "" {
				commit = comment.CommitID
			}
			if err := validateObjectID(commit); err != nil {
				return nil, nil, fmt.Errorf("github review comment commit is invalid")
			}
			thread.Comments = append(thread.Comments, contracts.PullRequestComment{ID: commentID(comment.ID), Author: githubUser(comment.User), Body: comment.Body, CommitSHA: commit})
		}
		threads = append(threads, thread)
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
	authority := make([]string, len(threads))
	for index := range threads {
		authority[index] = threads[index].ID
	}
	return threads, authority, nil
}

func anchorFor(comment reviewComment) (*contracts.PullRequestAnchor, error) {
	line := comment.OriginalLine
	if line == nil {
		line = comment.Line
	}
	if line == nil && comment.Path == "" {
		return nil, nil
	}
	if line == nil || comment.Path == "" {
		return nil, fmt.Errorf("github review comment anchor is incomplete")
	}
	return &contracts.PullRequestAnchor{Path: comment.Path, StartLine: *line, EndLine: *line}, nil
}

func githubUser(value ghUser) string { return "github-user-" + strconv.FormatInt(value.ID, 10) }
func commentID(value int64) string   { return "comment-" + strconv.FormatInt(value, 10) }

func digestBatch(batch pullrequest.ReviewBatch, threads []pullrequest.Thread) string {
	return digest(struct {
		Batch   pullrequest.ReviewBatch `json:"batch"`
		Threads []pullrequest.Thread    `json:"threads"`
	}{batch, threads})
}
func digestState(observation pullrequest.Observation) string {
	return digest(struct {
		State     contracts.PullRequestState        `json:"state"`
		Merge     contracts.PullRequestMergeability `json:"merge"`
		Source    string                            `json:"source"`
		Target    string                            `json:"target"`
		Iteration string                            `json:"iteration"`
	}{observation.State, observation.Mergeability, observation.SourceSHA, observation.TargetSHA, observation.Iteration})
}
func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeCursor(raw pullrequest.Cursor) (githubCursor, error) {
	if raw == "" {
		return githubCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(string(raw))
	if err != nil || len(decoded) == 0 || len(decoded) > 2048 {
		return githubCursor{}, fmt.Errorf("github cursor is malformed")
	}
	var cursor githubCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || requireEOF(decoder) != nil || cursor.Version != cursorVersion || !isDigest(cursor.StateSignature) || cursor.BatchDigest != "" && !isDigest(cursor.BatchDigest) {
		return githubCursor{}, fmt.Errorf("github cursor is malformed")
	}
	if cursor.Watermark.ID < 0 || cursor.Watermark.ID == 0 && (cursor.Watermark.SubmittedAt != "" || cursor.BatchDigest != "") || cursor.Watermark.ID > 0 && (cursor.Watermark.SubmittedAt == "" || parseCursorTime(cursor.Watermark.SubmittedAt).IsZero() || cursor.BatchDigest == "") {
		return githubCursor{}, fmt.Errorf("github cursor is malformed")
	}
	canonical, err := encodeCursor(cursor)
	if err != nil || canonical != string(raw) {
		return githubCursor{}, fmt.Errorf("github cursor is malformed")
	}
	return cursor, nil
}

func parseCursorTime(raw string) time.Time {
	instant, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || instant.UTC().Format(time.RFC3339Nano) != raw {
		return time.Time{}
	}
	return instant.UTC()
}
func encodeCursor(cursor githubCursor) (string, error) {
	if cursor.Version != cursorVersion || cursor.StateSignature == "" {
		return "", fmt.Errorf("github cursor is invalid")
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
func validateObjectID(value string) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("bad object id")
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return fmt.Errorf("bad object id")
		}
	}
	return nil
}
