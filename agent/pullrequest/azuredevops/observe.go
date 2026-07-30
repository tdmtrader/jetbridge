package azuredevops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	azureCursorVersion = 1
	maxIterations      = 256
	maxReviewers       = 512
	maxProviderThreads = 512
	maxThreadComments  = 256
)

type azureCursor struct {
	Version     int           `json:"v"`
	Watermark   voteWatermark `json:"w"`
	StateDigest string        `json:"s"`
	BatchDigest string        `json:"b"`
}

type voteWatermark struct {
	PublishedAt string `json:"a,omitempty"`
	ThreadID    int64  `json:"i,omitempty"`
}

type voteEvent struct {
	ThreadID   int64
	Published  time.Time
	ReviewerID string
	Vote       int
}

type reviewerState struct {
	ID          string
	Vote        int
	IsContainer bool
	Inactive    bool
}

func (observer *Observer) Observe(ctx context.Context, locator pullrequest.Locator, rawCursor pullrequest.Cursor) (pullrequest.Observation, error) {
	pullRequestID, err := observer.validateLocator(locator)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	cursor, err := decodeCursor(rawCursor)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	var providerPull azurePullRequest
	continuation, err := observer.getPage(ctx, observer.endpoint(pullRequestID, "", ""), &providerPull)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	if continuation != "" {
		return pullrequest.Observation{}, fmt.Errorf("azure devops pull request response unexpectedly paginated")
	}
	state, err := observer.validatePull(providerPull, pullRequestID)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	iterations, err := getCollection[azureIteration](ctx, observer, pullRequestID, "/iterations", maxIterations)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	latest, iterationCommits, err := selectLatestIteration(providerPull, iterations)
	if err != nil {
		return pullrequest.Observation{}, err
	}

	observation := pullrequest.Observation{
		Locator:      locator,
		URL:          observer.webURL(pullRequestID),
		State:        state,
		Mergeability: normalizeMergeStatus(providerPull.MergeStatus),
		SourceRef:    providerPull.SourceRefName,
		SourceSHA:    providerPull.LastMergeSourceCommit.CommitID,
		TargetRef:    providerPull.TargetRefName,
		TargetSHA:    providerPull.LastMergeTargetCommit.CommitID,
		Iteration:    strconv.FormatInt(latest.ID, 10),
	}

	var events []voteEvent
	var reviewers []reviewerState
	if state == contracts.PullRequestActive {
		providerThreads, err := getCollection[azureThread](ctx, observer, pullRequestID, "/threads", maxProviderThreads)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		providerReviewers, err := getCollection[azureReviewer](ctx, observer, pullRequestID, "/reviewers", maxReviewers)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		events, err = normalizeVoteEvents(providerThreads)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		reviewers, err = normalizeReviewers(providerReviewers)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		selected, err := selectWaitingTransition(events, reviewers, cursor.Watermark)
		if err != nil {
			return pullrequest.Observation{}, err
		}
		if selected != nil {
			threads, authority, err := normalizeFeedbackThreads(providerThreads, latest.ID, iterationCommits, selected.Published)
			if err != nil {
				return pullrequest.Observation{}, err
			}
			batch := pullrequest.ReviewBatch{
				ID:        "vote-" + strconv.FormatInt(selected.ThreadID, 10),
				ReviewID:  strconv.FormatInt(selected.ThreadID, 10),
				CommitSHA: latest.SourceRefCommit.CommitID,
				Reviewer:  azureUser(selected.ReviewerID),
				Ready:     true,
				ThreadIDs: authority,
			}
			observation.ReviewBatches = []pullrequest.ReviewBatch{batch}
			observation.Threads = threads
			cursor.Watermark = watermarkFor(*selected)
			cursor.BatchDigest = digestBatch(batch, threads)
		}
	}
	cursor.Version = azureCursorVersion
	cursor.StateDigest = digestProviderState(observation)
	encoded, err := encodeCursor(cursor)
	if err != nil {
		return pullrequest.Observation{}, err
	}
	observation.Cursor = pullrequest.Cursor(encoded)
	if err := observation.Validate(); err != nil {
		return pullrequest.Observation{}, fmt.Errorf("azure devops normalized observation is invalid: %w", err)
	}
	return observation, nil
}

func (observer *Observer) validateLocator(locator pullrequest.Locator) (string, error) {
	if locator.Provider != pullrequest.ProviderAzureDevOps {
		return "", fmt.Errorf("azure devops observer requires an azure-devops locator")
	}
	if locator.Repository != observer.project+"/"+observer.repositoryID {
		return "", fmt.Errorf("azure devops locator repository does not match configured project and repository")
	}
	number, err := strconv.ParseInt(locator.ExternalID, 10, 32)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != locator.ExternalID {
		return "", fmt.Errorf("azure devops pull request id must be a canonical positive int32")
	}
	return locator.ExternalID, nil
}

func (observer *Observer) validatePull(value azurePullRequest, expectedID string) (contracts.PullRequestState, error) {
	number, _ := strconv.ParseInt(expectedID, 10, 32)
	if value.PullRequestID != number || value.PullRequestID <= 0 || value.PullRequestID > math.MaxInt32 {
		return "", fmt.Errorf("azure devops pull request id does not match")
	}
	if value.Repository.ID != observer.repositoryID || value.Repository.IsFork {
		return "", fmt.Errorf("azure devops pull request repository does not match or is a fork")
	}
	if value.Repository.Project.Name != observer.project && value.Repository.Project.ID != observer.project {
		return "", fmt.Errorf("azure devops pull request project does not match")
	}
	if validateSegment("repository name", value.Repository.Name) != nil || validateSegment("project name", value.Repository.Project.Name) != nil || validateSegment("project id", value.Repository.Project.ID) != nil {
		return "", fmt.Errorf("azure devops pull request repository schema is invalid")
	}
	if len(value.ForkSource) > 0 && string(value.ForkSource) != "null" {
		return "", fmt.Errorf("azure devops fork pull requests are not supported")
	}
	if !value.SupportsIterations {
		return "", fmt.Errorf("azure devops pull request must support iterations")
	}
	if err := validateBranchRef(value.SourceRefName); err != nil {
		return "", fmt.Errorf("azure devops source ref is invalid")
	}
	if err := validateBranchRef(value.TargetRefName); err != nil {
		return "", fmt.Errorf("azure devops target ref is invalid")
	}
	if err := validateObjectID(value.LastMergeSourceCommit.CommitID); err != nil {
		return "", fmt.Errorf("azure devops source head is invalid")
	}
	if err := validateObjectID(value.LastMergeTargetCommit.CommitID); err != nil {
		return "", fmt.Errorf("azure devops target head is invalid")
	}
	return normalizeLifecycle(value.Status)
}

func normalizeLifecycle(value string) (contracts.PullRequestState, error) {
	switch value {
	case "active":
		return contracts.PullRequestActive, nil
	case "completed":
		return contracts.PullRequestCompleted, nil
	case "abandoned":
		return contracts.PullRequestAbandoned, nil
	default:
		return "", fmt.Errorf("azure devops pull request lifecycle is unknown")
	}
}

func normalizeMergeStatus(value string) contracts.PullRequestMergeability {
	switch value {
	case "succeeded":
		return contracts.PullRequestMergeable
	case "conflicts":
		return contracts.PullRequestConflicted
	case "rejectedByPolicy":
		return contracts.PullRequestPolicyBlocked
	default:
		return contracts.PullRequestUnknown
	}
}

func validateBranchRef(value string) error {
	if !strings.HasPrefix(value, "refs/heads/") || len(value) > 512 || strings.ContainsAny(value, " \t\r\n\x00\\~^:?*[") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") {
		return fmt.Errorf("invalid branch ref")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == "@" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("invalid branch ref")
		}
	}
	return nil
}

func validateObjectID(value string) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("invalid object id")
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return fmt.Errorf("invalid object id")
		}
	}
	return nil
}

func getCollection[T any](ctx context.Context, observer *Observer, pullRequestID, suffix string, maximum int) ([]T, error) {
	var output []T
	continuation := ""
	seen := map[string]struct{}{}
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber >= maxPages {
			return nil, fmt.Errorf("azure devops %s exceeds pagination limit", strings.TrimPrefix(suffix, "/"))
		}
		var page collectionPage[T]
		next, err := observer.getPage(ctx, observer.endpoint(pullRequestID, suffix, continuation), &page)
		if err != nil {
			return nil, err
		}
		if page.Count == nil || page.Value == nil || *page.Count < 0 || *page.Count != len(*page.Value) {
			return nil, fmt.Errorf("azure devops %s collection envelope is malformed", strings.TrimPrefix(suffix, "/"))
		}
		if len(output)+len(*page.Value) > maximum {
			return nil, fmt.Errorf("azure devops %s exceeds collection limit", strings.TrimPrefix(suffix, "/"))
		}
		output = append(output, (*page.Value)...)
		if next == "" {
			return output, nil
		}
		if _, duplicate := seen[next]; duplicate {
			return nil, fmt.Errorf("azure devops %s pagination repeats a token", strings.TrimPrefix(suffix, "/"))
		}
		seen[next] = struct{}{}
		continuation = next
	}
}

func selectLatestIteration(providerPull azurePullRequest, iterations []azureIteration) (azureIteration, map[int64]azureCommit, error) {
	if len(iterations) == 0 {
		return azureIteration{}, nil, fmt.Errorf("azure devops pull request has no iterations")
	}
	byID := make(map[int64]azureCommit, len(iterations))
	var latest azureIteration
	for _, iteration := range iterations {
		if iteration.ID <= 0 || iteration.ID > math.MaxInt32 {
			return azureIteration{}, nil, fmt.Errorf("azure devops iteration id is invalid")
		}
		if _, duplicate := byID[iteration.ID]; duplicate {
			return azureIteration{}, nil, fmt.Errorf("azure devops iteration id is duplicate")
		}
		if validateObjectID(iteration.SourceRefCommit.CommitID) != nil || validateObjectID(iteration.TargetRefCommit.CommitID) != nil {
			return azureIteration{}, nil, fmt.Errorf("azure devops iteration heads are invalid")
		}
		byID[iteration.ID] = iteration.SourceRefCommit
		if iteration.ID > latest.ID {
			latest = iteration
		}
	}
	if latest.SourceRefCommit.CommitID != providerPull.LastMergeSourceCommit.CommitID || latest.TargetRefCommit.CommitID != providerPull.LastMergeTargetCommit.CommitID {
		return azureIteration{}, nil, fmt.Errorf("azure devops highest iteration heads do not match pull request heads")
	}
	return latest, byID, nil
}

func normalizeReviewers(values []azureReviewer) ([]reviewerState, error) {
	output := make([]reviewerState, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentity(value.ID); err != nil {
			return nil, fmt.Errorf("azure devops reviewer id is invalid")
		}
		if !validVote(value.Vote) {
			return nil, fmt.Errorf("azure devops reviewer vote is invalid")
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return nil, fmt.Errorf("azure devops reviewer id is duplicate")
		}
		seen[value.ID] = struct{}{}
		output = append(output, reviewerState{ID: value.ID, Vote: value.Vote, IsContainer: value.IsContainer, Inactive: value.Inactive})
	}
	sort.Slice(output, func(i, j int) bool { return output[i].ID < output[j].ID })
	return output, nil
}

func normalizeVoteEvents(threads []azureThread) ([]voteEvent, error) {
	if err := validateProviderThreadIDs(threads); err != nil {
		return nil, err
	}
	events := make([]voteEvent, 0)
	for _, thread := range threads {
		threadType, hasType, err := stringProperty(thread.Properties, "CodeReviewThreadType")
		if err != nil {
			return nil, err
		}
		_, hasVoter := thread.Properties["CodeReviewVotedByTfId"]
		_, hasVote := thread.Properties["CodeReviewVoteResult"]
		if !hasType {
			if hasVoter || hasVote {
				return nil, fmt.Errorf("azure devops vote property bag has no thread type")
			}
			continue
		}
		if threadType != "VoteUpdate" {
			if hasVoter || hasVote {
				return nil, fmt.Errorf("azure devops non-vote thread carries vote properties")
			}
			continue
		}
		if thread.IsDeleted {
			return nil, fmt.Errorf("azure devops VoteUpdate thread is deleted")
		}
		reviewerID, present, err := stringProperty(thread.Properties, "CodeReviewVotedByTfId")
		if err != nil || !present || validateIdentity(reviewerID) != nil {
			return nil, fmt.Errorf("azure devops VoteUpdate reviewer is invalid")
		}
		rawVote, present, err := stringProperty(thread.Properties, "CodeReviewVoteResult")
		if err != nil || !present {
			return nil, fmt.Errorf("azure devops VoteUpdate result is invalid")
		}
		vote, err := strconv.Atoi(rawVote)
		if err != nil || strconv.Itoa(vote) != rawVote || !validVote(vote) {
			return nil, fmt.Errorf("azure devops VoteUpdate result is invalid")
		}
		published, err := parseProviderTime(thread.PublishedDate)
		if err != nil {
			return nil, fmt.Errorf("azure devops VoteUpdate publication time is invalid")
		}
		if err := validateSystemVoteComments(thread.Comments); err != nil {
			return nil, err
		}
		events = append(events, voteEvent{ThreadID: thread.ID, Published: published, ReviewerID: reviewerID, Vote: vote})
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Published.Equal(events[j].Published) {
			return events[i].ThreadID < events[j].ThreadID
		}
		return events[i].Published.Before(events[j].Published)
	})
	return events, nil
}

func validateProviderThreadIDs(threads []azureThread) error {
	seen := make(map[int64]struct{}, len(threads))
	for _, thread := range threads {
		if thread.ID <= 0 || thread.ID > math.MaxInt32 {
			return fmt.Errorf("azure devops thread id is invalid")
		}
		if _, duplicate := seen[thread.ID]; duplicate {
			return fmt.Errorf("azure devops thread id is duplicate")
		}
		seen[thread.ID] = struct{}{}
	}
	return nil
}

func stringProperty(properties map[string]azureProperty, name string) (string, bool, error) {
	property, found := properties[name]
	if !found {
		return "", false, nil
	}
	if property.Type != "System.String" || len(property.Value) == 0 {
		return "", true, fmt.Errorf("azure devops property %s is malformed", name)
	}
	var value string
	if err := json.Unmarshal(property.Value, &value); err != nil || strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > 512 {
		return "", true, fmt.Errorf("azure devops property %s is malformed", name)
	}
	return value, true, nil
}

func validateSystemVoteComments(comments []azureComment) error {
	if len(comments) == 0 || len(comments) > maxThreadComments {
		return fmt.Errorf("azure devops VoteUpdate comments are invalid")
	}
	byID := make(map[int64]azureComment, len(comments))
	live := 0
	for _, comment := range comments {
		if comment.ID <= 0 || comment.ID > math.MaxInt16 || comment.ParentCommentID < 0 || comment.ParentCommentID > math.MaxInt16 || comment.CommentType != "system" ||
			validateIdentity(comment.Author.ID) != nil || strings.TrimSpace(comment.Content) == "" || !utf8.ValidString(comment.Content) || len(comment.Content) > 32*1024 {
			return fmt.Errorf("azure devops VoteUpdate comment schema is invalid")
		}
		if _, err := parseProviderTime(comment.PublishedDate); err != nil {
			return fmt.Errorf("azure devops VoteUpdate comment schema is invalid")
		}
		if _, duplicate := byID[comment.ID]; duplicate {
			return fmt.Errorf("azure devops VoteUpdate comment id is duplicate")
		}
		byID[comment.ID] = comment
		if !comment.IsDeleted {
			live++
		}
	}
	for _, comment := range comments {
		seen := map[int64]struct{}{comment.ID: {}}
		parentID := comment.ParentCommentID
		for parentID != 0 {
			parent, found := byID[parentID]
			if !found {
				return fmt.Errorf("azure devops VoteUpdate reply has no parent")
			}
			if _, duplicate := seen[parent.ID]; duplicate {
				return fmt.Errorf("azure devops VoteUpdate reply cycle")
			}
			seen[parent.ID] = struct{}{}
			parentID = parent.ParentCommentID
		}
	}
	if live == 0 {
		return fmt.Errorf("azure devops VoteUpdate has no durable system comment")
	}
	return nil
}

func validVote(vote int) bool {
	switch vote {
	case -10, -5, 0, 5, 10:
		return true
	default:
		return false
	}
}

func selectWaitingTransition(events []voteEvent, reviewers []reviewerState, watermark voteWatermark) (*voteEvent, error) {
	if watermark.ThreadID != 0 {
		found := false
		for _, event := range events {
			if event.ThreadID == watermark.ThreadID && event.Published.UTC().Format(time.RFC3339Nano) == watermark.PublishedAt && event.Vote == -5 {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("azure devops cursor watermark is not present in durable vote history")
		}
	}
	current := make(map[string]reviewerState, len(reviewers))
	for _, reviewer := range reviewers {
		current[reviewer.ID] = reviewer
	}
	lastVote := make(map[string]int)
	episodes := make(map[string]voteEvent)
	for _, event := range events {
		previous := lastVote[event.ReviewerID]
		lastVote[event.ReviewerID] = event.Vote
		if event.Vote == -5 && previous != -5 {
			episodes[event.ReviewerID] = event
		} else if event.Vote != -5 {
			delete(episodes, event.ReviewerID)
		}
	}
	for reviewerID, vote := range lastVote {
		reviewer, found := current[reviewerID]
		// Removed reviewers remain in durable VoteUpdate history but no longer
		// appear in the current reviewer list. They cannot authorize a batch;
		// current reviewers, however, must exactly corroborate their last event.
		if found && reviewer.Vote != vote {
			return nil, fmt.Errorf("azure devops reviewer state does not corroborate durable vote history")
		}
	}
	candidates := make([]voteEvent, 0, len(episodes))
	for reviewerID, event := range episodes {
		reviewer, found := current[reviewerID]
		if !found {
			continue
		}
		if reviewer.Inactive || reviewer.IsContainer {
			return nil, fmt.Errorf("azure devops waiting vote is not from an active user reviewer")
		}
		if eventAfterWatermark(event, watermark) {
			candidates = append(candidates, event)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Published.Equal(candidates[j].Published) {
			return candidates[i].ThreadID < candidates[j].ThreadID
		}
		return candidates[i].Published.Before(candidates[j].Published)
	})
	if len(candidates) == 0 {
		return nil, nil
	}
	selected := candidates[0]
	return &selected, nil
}

func eventAfterWatermark(event voteEvent, watermark voteWatermark) bool {
	if watermark.ThreadID == 0 {
		return true
	}
	instant, err := time.Parse(time.RFC3339Nano, watermark.PublishedAt)
	if err != nil {
		return false
	}
	if event.Published.After(instant) {
		return true
	}
	return event.Published.Equal(instant) && event.ThreadID > watermark.ThreadID
}

func normalizeFeedbackThreads(providerThreads []azureThread, latestIteration int64, iterationCommits map[int64]azureCommit, completedAt time.Time) ([]pullrequest.Thread, []string, error) {
	output := make([]pullrequest.Thread, 0)
	authority := make([]string, 0)
	for _, providerThread := range providerThreads {
		_, systemThread, err := stringProperty(providerThread.Properties, "CodeReviewThreadType")
		if err != nil {
			return nil, nil, err
		}
		if systemThread || providerThread.IsDeleted {
			continue
		}
		switch providerThread.Status {
		case "pending":
			continue
		case "active", "fixed", "wontFix", "closed", "byDesign":
		default:
			return nil, nil, fmt.Errorf("azure devops user feedback thread status is unknown")
		}
		published, err := parseProviderTime(providerThread.PublishedDate)
		if err != nil {
			return nil, nil, fmt.Errorf("azure devops user feedback publication time is invalid")
		}
		if published.After(completedAt) {
			continue
		}
		if len(providerThread.Comments) == 0 || len(providerThread.Comments) > maxThreadComments {
			return nil, nil, fmt.Errorf("azure devops user feedback comments exceed bounds")
		}
		iterationID, commit, err := threadIteration(providerThread, latestIteration, iterationCommits)
		if err != nil {
			return nil, nil, err
		}
		anchor, err := normalizeAnchor(providerThread.ThreadContext)
		if err != nil {
			return nil, nil, err
		}
		comments, err := normalizeComments(providerThread.ID, providerThread.Comments, commit.CommitID, completedAt)
		if err != nil {
			return nil, nil, err
		}
		if len(comments) == 0 {
			continue
		}
		thread := pullrequest.Thread{
			ID:        "thread-" + strconv.FormatInt(providerThread.ID, 10),
			Iteration: strconv.FormatInt(iterationID, 10),
			Anchor:    anchor,
			Comments:  comments,
		}
		output = append(output, thread)
		if providerThread.Status == "active" {
			authority = append(authority, thread.ID)
		}
	}
	sort.Slice(output, func(i, j int) bool { return output[i].ID < output[j].ID })
	sort.Strings(authority)
	return output, authority, nil
}

func threadIteration(thread azureThread, latest int64, commits map[int64]azureCommit) (int64, azureCommit, error) {
	iterationID := latest
	if thread.PullRequestThreadContext != nil {
		context := thread.PullRequestThreadContext.IterationContext
		if context == nil || context.FirstComparingIteration <= 0 || context.SecondComparingIteration <= 0 || context.FirstComparingIteration > context.SecondComparingIteration {
			return 0, azureCommit{}, fmt.Errorf("azure devops feedback iteration context is invalid")
		}
		if _, found := commits[context.FirstComparingIteration]; !found {
			return 0, azureCommit{}, fmt.Errorf("azure devops feedback base iteration is unknown")
		}
		iterationID = context.SecondComparingIteration
	}
	commit, found := commits[iterationID]
	if !found {
		return 0, azureCommit{}, fmt.Errorf("azure devops feedback iteration is unknown")
	}
	return iterationID, commit, nil
}

func normalizeAnchor(context *azureThreadContext) (*contracts.PullRequestAnchor, error) {
	if context == nil {
		return nil, nil
	}
	left := context.LeftFileStart != nil || context.LeftFileEnd != nil
	right := context.RightFileStart != nil || context.RightFileEnd != nil
	if !left && !right && context.FilePath == "" {
		return nil, nil
	}
	if left == right || context.FilePath == "" {
		return nil, fmt.Errorf("azure devops feedback anchor is incomplete or ambiguous")
	}
	var start, end *azurePosition
	if right {
		start, end = context.RightFileStart, context.RightFileEnd
	} else {
		start, end = context.LeftFileStart, context.LeftFileEnd
	}
	if start == nil || end == nil || start.Line <= 0 || end.Line < start.Line || start.Offset < 0 || end.Offset < 0 {
		return nil, fmt.Errorf("azure devops feedback anchor is invalid")
	}
	filePath := strings.TrimPrefix(context.FilePath, "/")
	anchor := &contracts.PullRequestAnchor{Path: filePath, StartLine: start.Line, EndLine: end.Line}
	if err := anchor.Validate(); err != nil {
		return nil, fmt.Errorf("azure devops feedback anchor is invalid")
	}
	return anchor, nil
}

func normalizeComments(threadID int64, values []azureComment, commit string, completedAt time.Time) ([]contracts.PullRequestComment, error) {
	byID := make(map[int64]azureComment, len(values))
	for _, value := range values {
		if value.ID <= 0 || value.ID > math.MaxInt16 || value.ParentCommentID < 0 || value.ParentCommentID > math.MaxInt16 {
			return nil, fmt.Errorf("azure devops feedback comment id is invalid")
		}
		if _, duplicate := byID[value.ID]; duplicate {
			return nil, fmt.Errorf("azure devops feedback comment id is duplicate")
		}
		switch value.CommentType {
		case "text", "system", "codeChange":
		default:
			return nil, fmt.Errorf("azure devops feedback comment type is unknown")
		}
		byID[value.ID] = value
	}
	for _, value := range values {
		seen := map[int64]struct{}{value.ID: {}}
		parentID := value.ParentCommentID
		for parentID != 0 {
			parent, found := byID[parentID]
			if !found {
				return nil, fmt.Errorf("azure devops feedback reply has no parent")
			}
			if _, duplicate := seen[parent.ID]; duplicate {
				return nil, fmt.Errorf("azure devops feedback reply cycle")
			}
			seen[parent.ID] = struct{}{}
			parentID = parent.ParentCommentID
		}
	}
	output := make([]contracts.PullRequestComment, 0, len(values))
	for _, value := range values {
		if value.IsDeleted || value.CommentType != "text" {
			continue
		}
		published, err := parseProviderTime(value.PublishedDate)
		if err != nil {
			return nil, fmt.Errorf("azure devops feedback comment publication time is invalid")
		}
		if published.After(completedAt) {
			continue
		}
		if err := validateIdentity(value.Author.ID); err != nil {
			return nil, fmt.Errorf("azure devops feedback author is invalid")
		}
		comment := contracts.PullRequestComment{
			ID:        "comment-" + strconv.FormatInt(threadID, 10) + "-" + strconv.FormatInt(value.ID, 10),
			Author:    azureUser(value.Author.ID),
			Body:      value.Content,
			CommitSHA: commit,
		}
		if err := comment.Validate(); err != nil {
			return nil, fmt.Errorf("azure devops feedback comment is invalid")
		}
		output = append(output, comment)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].ID < output[j].ID })
	return output, nil
}

func validateIdentity(value string) error {
	if len(value) > 240 {
		return fmt.Errorf("identity is too long")
	}
	return contracts.ValidateIdentifier("azure devops identity", value)
}

func azureUser(value string) string {
	return "azure-user:" + value
}

func parseProviderTime(raw string) (time.Time, error) {
	if !strings.HasSuffix(raw, "Z") {
		return time.Time{}, fmt.Errorf("provider timestamp is not UTC")
	}
	instant, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return instant.UTC(), nil
}

func watermarkFor(event voteEvent) voteWatermark {
	return voteWatermark{PublishedAt: event.Published.UTC().Format(time.RFC3339Nano), ThreadID: event.ThreadID}
}

func digestBatch(batch pullrequest.ReviewBatch, threads []pullrequest.Thread) string {
	return digest(struct {
		Batch   pullrequest.ReviewBatch `json:"batch"`
		Threads []pullrequest.Thread    `json:"threads"`
	}{batch, threads})
}

func digestProviderState(observation pullrequest.Observation) string {
	return digest(struct {
		State     contracts.PullRequestState        `json:"state"`
		Merge     contracts.PullRequestMergeability `json:"merge"`
		SourceSHA string                            `json:"source_sha"`
		TargetSHA string                            `json:"target_sha"`
		Iteration string                            `json:"iteration"`
	}{
		observation.State,
		observation.Mergeability,
		observation.SourceSHA,
		observation.TargetSHA,
		observation.Iteration,
	})
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeCursor(raw pullrequest.Cursor) (azureCursor, error) {
	if raw == "" {
		return azureCursor{}, nil
	}
	if err := raw.Validate(); err != nil {
		return azureCursor{}, fmt.Errorf("azure devops cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(string(raw))
	if err != nil || len(decoded) == 0 || len(decoded) > 2048 {
		return azureCursor{}, fmt.Errorf("azure devops cursor is malformed")
	}
	var cursor azureCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || requireEOF(decoder) != nil || cursor.Version != azureCursorVersion || !isDigest(cursor.StateDigest) || cursor.BatchDigest != "" && !isDigest(cursor.BatchDigest) {
		return azureCursor{}, fmt.Errorf("azure devops cursor is malformed")
	}
	if cursor.Watermark.ThreadID < 0 ||
		cursor.Watermark.ThreadID == 0 && (cursor.Watermark.PublishedAt != "" || cursor.BatchDigest != "") ||
		cursor.Watermark.ThreadID > 0 && (parseCursorTime(cursor.Watermark.PublishedAt).IsZero() || cursor.BatchDigest == "") {
		return azureCursor{}, fmt.Errorf("azure devops cursor is malformed")
	}
	canonical, err := encodeCursor(cursor)
	if err != nil || canonical != string(raw) {
		return azureCursor{}, fmt.Errorf("azure devops cursor is malformed")
	}
	return cursor, nil
}

func encodeCursor(cursor azureCursor) (string, error) {
	if cursor.Version != azureCursorVersion || !isDigest(cursor.StateDigest) {
		return "", fmt.Errorf("azure devops cursor is invalid")
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func parseCursorTime(raw string) time.Time {
	instant, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || instant.UTC().Format(time.RFC3339Nano) != raw {
		return time.Time{}
	}
	return instant.UTC()
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
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
