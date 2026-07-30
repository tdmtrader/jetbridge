package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	maxMutationRequestBytes = 64 << 10
	maxMutationItems        = 1024
	azureCommentTypeText    = 1
	azureThreadStatusActive = 1
)

var (
	ErrStaleSource = errors.New("azure devops source head is stale")
	ErrStaleTarget = errors.New("azure devops target head is stale")

	mutationOperationKeyPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	mutationMarkerPattern       = regexp.MustCompile(
		`^<!-- Jetbridge-Operation: (create_pr sha256:[0-9a-f]{64}|respond_to_review sha256:[0-9a-f]{64} (summary|thread thread-[1-9][0-9]*)) -->$`,
	)
)

const mutationOperationMetadata = "Jetbridge-Operation:"

// Adapter implements the complete Azure DevOps REST 7.1 provider boundary.
// It intentionally exposes no completion or abandonment operation.
type Adapter struct {
	*Observer
	*Mutator
}

// Mutator implements only exact provider-native branch and pull-request
// mutations. Azure DevOps is contract-tested against REST 7.1, not
// live-validated.
type Mutator struct {
	observer *Observer
}

func New(
	organizationURL string,
	project string,
	repositoryID string,
	token pullrequest.TokenSource,
	client *http.Client,
) (*Adapter, error) {
	observer, err := NewObserver(
		organizationURL, project, repositoryID, token, client,
	)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		Observer: observer,
		Mutator:  &Mutator{observer: observer},
	}, nil
}

func NewMutator(
	organizationURL string,
	project string,
	repositoryID string,
	token pullrequest.TokenSource,
	client *http.Client,
) (*Mutator, error) {
	adapter, err := New(organizationURL, project, repositoryID, token, client)
	if err != nil {
		return nil, err
	}
	return adapter.Mutator, nil
}

func (mutator *Mutator) CompareAndSwapBranch(
	ctx context.Context,
	mutation pullrequest.BranchMutation,
) (pullrequest.BranchResult, error) {
	if mutator == nil || mutator.observer == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("azure devops mutator is required")
	}
	if err := mutator.validateBranchMutation(mutation); err != nil {
		return pullrequest.BranchResult{}, err
	}

	targetHead, targetExists, err := mutator.exactRef(ctx, mutation.TargetRef)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	if !targetExists || targetHead != mutation.ExpectedTargetSHA {
		return pullrequest.BranchResult{}, ErrStaleTarget
	}

	sourceHead, sourceExists, err := mutator.exactRef(ctx, mutation.Ref)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	if sourceExists && sourceHead == mutation.NewSourceSHA {
		return pullrequest.BranchResult{
			HeadSHA: sourceHead,
			Applied: false,
		}, nil
	}
	if sourceExists != mutation.ExpectedSource.Exists ||
		sourceExists && sourceHead != mutation.ExpectedSource.SHA {
		return pullrequest.BranchResult{}, ErrStaleSource
	}

	oldObjectID := mutation.ExpectedSource.SHA
	if !mutation.ExpectedSource.Exists {
		oldObjectID = strings.Repeat("0", len(mutation.NewSourceSHA))
	}
	target := mutator.endpoint("/refs", nil)
	var page collectionPage[azureRefUpdateResult]
	if err := mutator.doNonPaginatedJSON(ctx, http.MethodPost, target, []azureRefUpdate{{
		Name:        mutation.Ref,
		OldObjectID: oldObjectID,
		NewObjectID: mutation.NewSourceSHA,
	}}, &page, "ref update"); err != nil {
		return pullrequest.BranchResult{}, err
	}
	if page.Count == nil || page.Value == nil ||
		*page.Count != len(*page.Value) || len(*page.Value) != 1 {
		return pullrequest.BranchResult{}, fmt.Errorf("azure devops ref update returned an ambiguous result")
	}
	result := (*page.Value)[0]
	if result.RepositoryID != mutator.observer.repositoryID ||
		result.Name != mutation.Ref ||
		result.NewObjectID != mutation.NewSourceSHA ||
		validateObjectID(result.OldObjectID) != nil {
		return pullrequest.BranchResult{}, fmt.Errorf("azure devops ref update result does not match the operation")
	}
	switch result.UpdateStatus {
	case "succeeded":
		if !result.Success || result.OldObjectID != oldObjectID {
			return pullrequest.BranchResult{}, fmt.Errorf("azure devops ref update success flag is inconsistent")
		}
		return pullrequest.BranchResult{
			HeadSHA: mutation.NewSourceSHA,
			Applied: true,
		}, nil
	case "staleOldObjectId":
		if result.Success {
			return pullrequest.BranchResult{}, fmt.Errorf("azure devops stale ref update success flag is inconsistent")
		}
		return pullrequest.BranchResult{}, ErrStaleSource
	default:
		return pullrequest.BranchResult{}, fmt.Errorf(
			"azure devops ref update status is unknown",
		)
	}
}

func (mutator *Mutator) FindOrCreatePullRequest(
	ctx context.Context,
	request pullrequest.CreateRequest,
) (pullrequest.ExternalPullRequest, error) {
	if mutator == nil || mutator.observer == nil {
		return pullrequest.ExternalPullRequest{}, fmt.Errorf("azure devops mutator is required")
	}
	if err := mutator.validateCreateRequest(request); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	marker := operationMarker("create_pr", request.OperationKey, "")
	matches, err := mutator.findPullRequests(ctx, request, marker)
	if err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	switch len(matches) {
	case 1:
		return mutator.externalPullRequest(request, matches[0])
	case 0:
	default:
		return pullrequest.ExternalPullRequest{}, fmt.Errorf(
			"azure devops pull request operation marker is ambiguous",
		)
	}

	target := mutator.endpoint("/pullRequests", nil)
	var created azurePullRequest
	if err := mutator.doNonPaginatedJSON(
		ctx,
		http.MethodPost,
		target,
		struct {
			SourceRefName string `json:"sourceRefName"`
			TargetRefName string `json:"targetRefName"`
			Title         string `json:"title"`
			Description   string `json:"description"`
		}{
			SourceRefName: request.SourceRef,
			TargetRefName: request.TargetRef,
			Title:         request.Title,
			Description:   appendOperationMarker(request.Body, marker),
		},
		&created,
		"pull request creation",
	); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	if err := mutator.validateExactPull(request, created, marker); err != nil {
		return pullrequest.ExternalPullRequest{}, fmt.Errorf(
			"azure devops created pull request does not match the operation: %w",
			err,
		)
	}
	return mutator.externalPullRequest(request, created)
}

func (mutator *Mutator) PublishValidationStatus(
	ctx context.Context,
	request pullrequest.StatusRequest,
) (pullrequest.ExternalResult, error) {
	if mutator == nil || mutator.observer == nil {
		return pullrequest.ExternalResult{}, fmt.Errorf("azure devops mutator is required")
	}
	providerState, err := mutator.validateStatusRequest(request)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	iterations, err := mutator.mutationIterations(ctx, request.Locator.ExternalID)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	statuses, err := readMutationCollection[azurePullRequestStatus](
		ctx,
		mutator,
		mutator.endpoint(
			"/pullRequests/"+request.Locator.ExternalID+"/statuses", nil,
		),
	)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	contextName := "jetbridge/" + request.OperationKey
	var recovered *azurePullRequestStatus
	for index := range statuses {
		status := statuses[index]
		if status.Context.Name != contextName {
			if strings.Contains(status.Context.Name, request.OperationKey) {
				return pullrequest.ExternalResult{}, fmt.Errorf(
					"azure devops status operation context was altered",
				)
			}
			continue
		}
		iteration, found := iterations[status.IterationID]
		if status.ID <= 0 ||
			status.Context.Genre != "jetbridge" ||
			status.State != providerState ||
			status.Description != request.Description ||
			status.TargetURL != request.TargetURL ||
			!found ||
			iteration.SourceRefCommit.CommitID != request.SourceSHA {
			return pullrequest.ExternalResult{}, fmt.Errorf(
				"azure devops status operation context was altered",
			)
		}
		if recovered == nil {
			value := status
			recovered = &value
		}
	}
	if recovered != nil {
		return mutator.externalStatus(
			request.Locator.ExternalID, request.OperationKey, *recovered,
		), nil
	}

	providerPull, err := mutator.getPullRequest(
		ctx, request.Locator.ExternalID,
	)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if providerPull.LastMergeSourceCommit.CommitID != request.SourceSHA {
		return pullrequest.ExternalResult{}, fmt.Errorf(
			"azure devops status source head is stale",
		)
	}
	latestID, err := exactCurrentIteration(providerPull, iterations)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}

	payload := azurePullRequestStatus{
		State:       providerState,
		Description: request.Description,
		TargetURL:   request.TargetURL,
		Context: azureStatusContext{
			Name: contextName, Genre: "jetbridge",
		},
		IterationID: latestID,
	}
	target := mutator.endpoint(
		"/pullRequests/"+request.Locator.ExternalID+"/statuses", nil,
	)
	var created azurePullRequestStatus
	if err := mutator.doNonPaginatedJSON(
		ctx,
		http.MethodPost,
		target,
		payload,
		&created,
		"status creation",
	); err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if created.ID <= 0 ||
		created.State != payload.State ||
		created.Description != payload.Description ||
		created.TargetURL != payload.TargetURL ||
		created.Context != payload.Context ||
		created.IterationID != payload.IterationID {
		return pullrequest.ExternalResult{}, fmt.Errorf(
			"azure devops created status does not match the operation",
		)
	}
	return mutator.externalStatus(
		request.Locator.ExternalID, request.OperationKey, created,
	), nil
}

func (mutator *Mutator) PublishReviewResponse(
	ctx context.Context,
	request pullrequest.ResponseRequest,
) (pullrequest.ExternalResult, error) {
	if mutator == nil || mutator.observer == nil {
		return pullrequest.ExternalResult{}, fmt.Errorf("azure devops mutator is required")
	}
	threadIDs, err := mutator.validateResponseRequest(request)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if _, err := mutator.getPullRequest(
		ctx, request.Locator.ExternalID,
	); err != nil {
		return pullrequest.ExternalResult{}, err
	}
	threads, err := readMutationCollection[azureThread](
		ctx,
		mutator,
		mutator.endpoint(
			"/pullRequests/"+request.Locator.ExternalID+"/threads", nil,
		),
	)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if err := validateRecoveryThreads(threads, threadIDs); err != nil {
		return pullrequest.ExternalResult{}, err
	}
	threadRoots, err := responseThreadRoots(
		threads, threadIDs, request.Response.Replies,
	)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}

	summaryMarker := operationMarker(
		"respond_to_review", request.OperationKey, "summary",
	)
	authorizedProviderThreads := make(map[int64]struct{}, len(threadIDs))
	for _, threadID := range threadIDs {
		authorizedProviderThreads[threadID] = struct{}{}
	}
	rootCommentID := int64(0)
	found, err := recoverAzureMarker(
		threads,
		summaryMarker,
		request.OperationKey,
		nil,
		&rootCommentID,
		authorizedProviderThreads,
	)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if !found {
		content := appendOperationMarker(
			request.Response.Summary, summaryMarker,
		)
		target := mutator.endpoint(
			"/pullRequests/"+request.Locator.ExternalID+"/threads", nil,
		)
		var created azureThread
		if err := mutator.doNonPaginatedJSON(
			ctx,
			http.MethodPost,
			target,
			struct {
				Comments []azureCommentMutation `json:"comments"`
				Status   int                    `json:"status"`
			}{
				Comments: []azureCommentMutation{{
					ParentCommentID: 0,
					Content:         content,
					CommentType:     azureCommentTypeText,
				}},
				Status: azureThreadStatusActive,
			},
			&created,
			"summary thread creation",
		); err != nil {
			return pullrequest.ExternalResult{}, err
		}
		if _, sealed := authorizedProviderThreads[created.ID]; sealed ||
			created.ID <= 0 ||
			!containsExactCreatedComment(created.Comments, content) {
			return pullrequest.ExternalResult{}, fmt.Errorf(
				"azure devops created summary thread does not match the operation",
			)
		}
	}

	for _, reply := range request.Response.Replies {
		threadID := threadIDs[reply.ThreadID]
		threadRoot := threadRoots[reply.ThreadID]
		marker := operationMarker(
			"respond_to_review",
			request.OperationKey,
			"thread "+reply.ThreadID,
		)
		found, err := recoverAzureMarker(
			threads,
			marker,
			request.OperationKey,
			&threadID,
			&threadRoot,
			nil,
		)
		if err != nil {
			return pullrequest.ExternalResult{}, err
		}
		if found {
			continue
		}
		content := appendOperationMarker(reply.Body, marker)
		target := mutator.endpoint(
			"/pullRequests/"+request.Locator.ExternalID+
				"/threads/"+strconv.FormatInt(threadID, 10)+"/comments",
			nil,
		)
		var created azureComment
		if err := mutator.doNonPaginatedJSON(
			ctx,
			http.MethodPost,
			target,
			azureCommentMutation{
				ParentCommentID: threadRoot,
				Content:         content,
				CommentType:     azureCommentTypeText,
			},
			&created,
			"thread response creation",
		); err != nil {
			return pullrequest.ExternalResult{}, err
		}
		if created.ID <= 0 || created.ParentCommentID != threadRoot ||
			created.Content != content || created.IsDeleted ||
			created.CommentType != "text" {
			return pullrequest.ExternalResult{}, fmt.Errorf(
				"azure devops created thread response does not match the operation",
			)
		}
	}

	return pullrequest.ExternalResult{
		OperationKey: request.OperationKey,
		ExternalID:   request.Batch.ID,
		URL:          mutator.observer.webURL(request.Locator.ExternalID),
	}, nil
}

type azureCommentMutation struct {
	ParentCommentID int64  `json:"parentCommentId"`
	Content         string `json:"content"`
	CommentType     int    `json:"commentType"`
}

func (mutator *Mutator) validateResponseRequest(
	request pullrequest.ResponseRequest,
) (map[string]int64, error) {
	if err := mutator.validateMutationLocator(request.Locator, true); err != nil {
		return nil, err
	}
	if !mutationOperationKeyPattern.MatchString(request.OperationKey) {
		return nil, fmt.Errorf("azure devops response operation key is invalid")
	}
	known := make(map[string]struct{}, len(request.Batch.ThreadIDs))
	for _, threadID := range request.Batch.ThreadIDs {
		known[threadID] = struct{}{}
	}
	if err := request.Batch.Validate(known); err != nil {
		return nil, fmt.Errorf("azure devops review batch is invalid: %w", err)
	}
	if err := request.Response.Validate(nil); err != nil {
		return nil, fmt.Errorf("azure devops review response is invalid: %w", err)
	}
	if request.Response.BatchID != request.Batch.ID ||
		strings.Contains(
			request.Response.Summary, mutationOperationMetadata,
		) {
		return nil, fmt.Errorf(
			"azure devops review response is not authorized by the batch",
		)
	}
	output := make(map[string]int64, len(request.Batch.ThreadIDs))
	for _, threadID := range request.Batch.ThreadIDs {
		value, err := parseAzureThreadID(threadID)
		if err != nil {
			return nil, fmt.Errorf(
				"azure devops review batch thread is invalid",
			)
		}
		output[threadID] = value
	}
	for _, reply := range request.Response.Replies {
		if _, authorized := output[reply.ThreadID]; !authorized ||
			strings.Contains(reply.Body, mutationOperationMetadata) {
			return nil, fmt.Errorf(
				"azure devops review response thread is not authorized",
			)
		}
	}
	return output, nil
}

func parseAzureThreadID(threadID string) (int64, error) {
	raw := strings.TrimPrefix(threadID, "thread-")
	value, err := strconv.ParseInt(raw, 10, 32)
	if raw == threadID || err != nil || value <= 0 ||
		strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf("not a canonical azure devops thread")
	}
	return value, nil
}

func validateRecoveryThreads(
	threads []azureThread,
	authorized map[string]int64,
) error {
	if len(threads) > maxProviderThreads {
		return fmt.Errorf("azure devops response threads exceed item limit")
	}
	seenThreads := make(map[int64]struct{}, len(threads))
	available := make(map[int64]struct{}, len(threads))
	for _, thread := range threads {
		if thread.ID <= 0 {
			return fmt.Errorf("azure devops response thread id is invalid")
		}
		if _, duplicate := seenThreads[thread.ID]; duplicate {
			return fmt.Errorf("azure devops response thread id is duplicate")
		}
		seenThreads[thread.ID] = struct{}{}
		if !thread.IsDeleted {
			available[thread.ID] = struct{}{}
		}
		seenComments := make(map[int64]struct{}, len(thread.Comments))
		if len(thread.Comments) > maxThreadComments {
			return fmt.Errorf("azure devops response thread has too many comments")
		}
		for _, comment := range thread.Comments {
			if comment.ID <= 0 || comment.ParentCommentID < 0 ||
				!utf8.ValidString(comment.Content) ||
				len(comment.Content) > 64<<10 {
				return fmt.Errorf("azure devops response comment is invalid")
			}
			if _, duplicate := seenComments[comment.ID]; duplicate {
				return fmt.Errorf("azure devops response comment is duplicate")
			}
			seenComments[comment.ID] = struct{}{}
			switch comment.CommentType {
			case "text", "system", "codeChange":
			default:
				return fmt.Errorf(
					"azure devops response comment type is unknown",
				)
			}
		}
	}
	for _, threadID := range authorized {
		if _, exists := available[threadID]; !exists {
			return fmt.Errorf(
				"azure devops authorized response thread is unavailable",
			)
		}
	}
	return nil
}

func responseThreadRoots(
	threads []azureThread,
	authorized map[string]int64,
	replies []contracts.PullRequestThreadResponse,
) (map[string]int64, error) {
	byID := make(map[int64]azureThread, len(threads))
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	output := make(map[string]int64, len(replies))
	for _, reply := range replies {
		threadID, found := authorized[reply.ThreadID]
		if !found {
			return nil, fmt.Errorf(
				"azure devops response thread is not authorized",
			)
		}
		thread, found := byID[threadID]
		if !found || thread.IsDeleted {
			return nil, fmt.Errorf(
				"azure devops response thread is unavailable",
			)
		}
		var root int64
		for _, comment := range thread.Comments {
			if comment.ParentCommentID != 0 ||
				comment.CommentType != "text" ||
				comment.IsDeleted {
				continue
			}
			if root != 0 {
				return nil, fmt.Errorf(
					"azure devops response thread root is ambiguous",
				)
			}
			root = comment.ID
		}
		if root <= 0 {
			return nil, fmt.Errorf(
				"azure devops response thread root is unavailable",
			)
		}
		output[reply.ThreadID] = root
	}
	return output, nil
}

func recoverAzureMarker(
	threads []azureThread,
	marker string,
	operationKey string,
	expectedThreadID *int64,
	expectedParentCommentID *int64,
	forbiddenThreadIDs map[int64]struct{},
) (bool, error) {
	matches := 0
	for _, thread := range threads {
		for _, comment := range thread.Comments {
			exact, err := exactOperationMarker(
				comment.Content, marker, operationKey,
			)
			if err != nil {
				return false, err
			}
			if !exact {
				continue
			}
			if _, forbidden := forbiddenThreadIDs[thread.ID]; forbidden {
				return false, fmt.Errorf(
					"azure devops summary operation marker belongs to an authorized review thread",
				)
			}
			if comment.IsDeleted ||
				comment.CommentType != "text" ||
				expectedThreadID != nil &&
					thread.ID != *expectedThreadID ||
				expectedParentCommentID != nil &&
					comment.ParentCommentID != *expectedParentCommentID {
				return false, fmt.Errorf(
					"azure devops response operation marker belongs to another thread or comment",
				)
			}
			matches++
		}
	}
	if matches > 1 {
		return false, fmt.Errorf(
			"azure devops response operation marker is ambiguous",
		)
	}
	return matches == 1, nil
}

func containsExactCreatedComment(
	comments []azureComment,
	content string,
) bool {
	matches := 0
	for _, comment := range comments {
		if comment.ID > 0 && comment.ParentCommentID == 0 &&
			comment.Content == content && !comment.IsDeleted &&
			comment.CommentType == "text" {
			matches++
		}
	}
	return matches == 1
}

func (mutator *Mutator) mutationIterations(
	ctx context.Context,
	pullRequestID string,
) (map[int64]azureIteration, error) {
	values, err := readMutationCollection[azureIteration](
		ctx,
		mutator,
		mutator.endpoint(
			"/pullRequests/"+pullRequestID+"/iterations", nil,
		),
	)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 || len(values) > maxIterations {
		return nil, fmt.Errorf("azure devops status iterations are invalid")
	}
	output := make(map[int64]azureIteration, len(values))
	for _, value := range values {
		if value.ID <= 0 ||
			validateObjectID(value.SourceRefCommit.CommitID) != nil ||
			validateObjectID(value.TargetRefCommit.CommitID) != nil {
			return nil, fmt.Errorf("azure devops status iteration is invalid")
		}
		if _, duplicate := output[value.ID]; duplicate {
			return nil, fmt.Errorf("azure devops status iteration is duplicate")
		}
		output[value.ID] = value
	}
	return output, nil
}

func exactCurrentIteration(
	providerPull azurePullRequest,
	iterations map[int64]azureIteration,
) (int64, error) {
	var latest int64
	for id := range iterations {
		if id > latest {
			latest = id
		}
	}
	iteration, found := iterations[latest]
	if latest <= 0 || !found ||
		iteration.SourceRefCommit.CommitID !=
			providerPull.LastMergeSourceCommit.CommitID ||
		iteration.TargetRefCommit.CommitID !=
			providerPull.LastMergeTargetCommit.CommitID {
		return 0, fmt.Errorf(
			"azure devops latest iteration does not match the current pull request heads",
		)
	}
	return latest, nil
}

func (mutator *Mutator) externalStatus(
	pullRequestID string,
	operationKey string,
	status azurePullRequestStatus,
) pullrequest.ExternalResult {
	return pullrequest.ExternalResult{
		OperationKey: operationKey,
		ExternalID:   strconv.FormatInt(status.ID, 10),
		URL:          mutator.observer.webURL(pullRequestID),
	}
}

func (mutator *Mutator) validateStatusRequest(
	request pullrequest.StatusRequest,
) (string, error) {
	if err := mutator.validateMutationLocator(request.Locator, true); err != nil {
		return "", err
	}
	if validateObjectID(request.SourceSHA) != nil ||
		!mutationOperationKeyPattern.MatchString(request.OperationKey) ||
		!boundedMutationText(request.Description, 140, false) ||
		!validAbsoluteMutationURL(request.TargetURL) {
		return "", fmt.Errorf("azure devops status operation is invalid")
	}
	switch request.State {
	case "error":
		return "error", nil
	case "failure":
		return "failed", nil
	case "pending":
		return "pending", nil
	case "success":
		return "succeeded", nil
	default:
		return "", fmt.Errorf("azure devops status state is invalid")
	}
}

func (mutator *Mutator) findPullRequests(
	ctx context.Context,
	request pullrequest.CreateRequest,
	marker string,
) ([]azurePullRequest, error) {
	target := mutator.endpoint("/pullRequests", url.Values{
		"searchCriteria.sourceRefName": []string{request.SourceRef},
		"searchCriteria.status":        []string{"all"},
		"searchCriteria.targetRefName": []string{request.TargetRef},
	})
	candidates, err := readMutationCollection[azurePullRequest](
		ctx, mutator, target,
	)
	if err != nil {
		return nil, err
	}
	matches := make([]azurePullRequest, 0, 1)
	seen := make(map[int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.PullRequestID <= 0 ||
			candidate.Repository.ID != mutator.observer.repositoryID ||
			(candidate.Repository.Project.Name != mutator.observer.project &&
				candidate.Repository.Project.ID != mutator.observer.project) ||
			candidate.Repository.IsFork ||
			candidate.SourceRefName != request.SourceRef ||
			candidate.TargetRefName != request.TargetRef {
			return nil, fmt.Errorf(
				"azure devops pull request search returned an invalid candidate",
			)
		}
		if _, duplicate := seen[candidate.PullRequestID]; duplicate {
			return nil, fmt.Errorf(
				"azure devops pull request search returned a duplicate candidate",
			)
		}
		seen[candidate.PullRequestID] = struct{}{}
		detailed, err := mutator.getPullRequest(
			ctx, strconv.FormatInt(candidate.PullRequestID, 10),
		)
		if err != nil {
			return nil, err
		}
		if err := mutator.validateExactPull(
			request, detailed, marker,
		); err != nil {
			return nil, fmt.Errorf(
				"azure devops matching pull request does not match its exact operation: %w",
				err,
			)
		}
		matches = append(matches, detailed)
	}
	return matches, nil
}

func (mutator *Mutator) getPullRequest(
	ctx context.Context,
	pullRequestID string,
) (azurePullRequest, error) {
	if _, err := strconv.ParseInt(pullRequestID, 10, 32); err != nil {
		return azurePullRequest{}, fmt.Errorf("azure devops pull request id is invalid")
	}
	target := mutator.endpoint("/pullRequests/"+pullRequestID, nil)
	var value azurePullRequest
	if continuation, err := mutator.doJSON(
		ctx, http.MethodGet, target, nil, &value,
	); err != nil {
		return azurePullRequest{}, err
	} else if continuation != "" {
		return azurePullRequest{}, fmt.Errorf(
			"azure devops pull request detail unexpectedly paginated",
		)
	}
	if _, err := mutator.observer.validatePull(value, pullRequestID); err != nil {
		return azurePullRequest{}, err
	}
	return value, nil
}

func (mutator *Mutator) validateExactPull(
	request pullrequest.CreateRequest,
	value azurePullRequest,
	marker string,
) error {
	pullRequestID := strconv.FormatInt(value.PullRequestID, 10)
	if _, err := mutator.observer.validatePull(value, pullRequestID); err != nil {
		return err
	}
	exact, err := exactOperationMarker(
		value.Description, marker, request.OperationKey,
	)
	if err != nil {
		return err
	}
	if !exact ||
		value.SourceRefName != request.SourceRef ||
		value.LastMergeSourceCommit.CommitID != request.SourceSHA ||
		value.TargetRefName != request.TargetRef ||
		value.LastMergeTargetCommit.CommitID != request.TargetSHA {
		return fmt.Errorf("provider refs, heads, or marker differ")
	}
	return nil
}

func (mutator *Mutator) externalPullRequest(
	request pullrequest.CreateRequest,
	value azurePullRequest,
) (pullrequest.ExternalPullRequest, error) {
	pullRequestID := strconv.FormatInt(value.PullRequestID, 10)
	state, err := mutator.observer.validatePull(value, pullRequestID)
	if err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	locator := request.Locator
	locator.ExternalID = pullRequestID
	return pullrequest.ExternalPullRequest{
		Locator:   locator,
		URL:       mutator.observer.webURL(pullRequestID),
		State:     state,
		SourceRef: value.SourceRefName,
		SourceSHA: value.LastMergeSourceCommit.CommitID,
		TargetRef: value.TargetRefName,
		TargetSHA: value.LastMergeTargetCommit.CommitID,
	}, nil
}

func (mutator *Mutator) validateCreateRequest(
	request pullrequest.CreateRequest,
) error {
	if err := mutator.validateMutationLocator(request.Locator, false); err != nil {
		return err
	}
	if request.Locator.ExternalID != "" ||
		validateBranchRef(request.SourceRef) != nil ||
		validateBranchRef(request.TargetRef) != nil ||
		request.SourceRef == request.TargetRef ||
		validateObjectID(request.SourceSHA) != nil ||
		validateObjectID(request.TargetSHA) != nil ||
		len(request.SourceSHA) != len(request.TargetSHA) ||
		!mutationOperationKeyPattern.MatchString(request.OperationKey) {
		return fmt.Errorf("azure devops pull request creation authority is invalid")
	}
	if !boundedMutationText(request.Title, 256, false) ||
		!boundedMutationText(request.Body, 32<<10, true) ||
		strings.Contains(request.Body, mutationOperationMetadata) {
		return fmt.Errorf("azure devops pull request metadata is invalid")
	}
	return nil
}

func (mutator *Mutator) exactRef(
	ctx context.Context,
	refName string,
) (string, bool, error) {
	target := mutator.endpoint("/refs", url.Values{
		"filter": []string{strings.TrimPrefix(refName, "refs/")},
	})
	values, err := readMutationCollection[azureRef](ctx, mutator, target)
	if err != nil {
		return "", false, err
	}
	var exact *azureRef
	for index := range values {
		candidate := values[index]
		if validateBranchRef(candidate.Name) != nil ||
			validateObjectID(candidate.ObjectID) != nil {
			return "", false, fmt.Errorf("azure devops exact ref response is invalid")
		}
		if candidate.Name != refName {
			continue
		}
		if exact != nil {
			return "", false, fmt.Errorf("azure devops exact ref response is ambiguous")
		}
		value := candidate
		exact = &value
	}
	if exact == nil {
		return "", false, nil
	}
	return exact.ObjectID, true, nil
}

func (mutator *Mutator) validateBranchMutation(
	mutation pullrequest.BranchMutation,
) error {
	if err := mutator.validateMutationLocator(mutation.Locator, false); err != nil {
		return err
	}
	if validateBranchRef(mutation.Ref) != nil ||
		validateBranchRef(mutation.TargetRef) != nil ||
		mutation.Ref == mutation.TargetRef ||
		mutation.ExpectedSource.Validate() != nil ||
		validateObjectID(mutation.ExpectedTargetSHA) != nil ||
		validateObjectID(mutation.NewSourceSHA) != nil ||
		len(mutation.ExpectedTargetSHA) != len(mutation.NewSourceSHA) ||
		mutation.ExpectedSource.Exists &&
			len(mutation.ExpectedSource.SHA) != len(mutation.NewSourceSHA) ||
		!mutationOperationKeyPattern.MatchString(mutation.OperationKey) {
		return fmt.Errorf("azure devops branch mutation authority is invalid")
	}
	return nil
}

func (mutator *Mutator) validateMutationLocator(
	locator pullrequest.Locator,
	requireExternalID bool,
) error {
	if locator.Provider != pullrequest.ProviderAzureDevOps {
		return fmt.Errorf("azure devops mutator requires an azure-devops locator")
	}
	if locator.Repository !=
		mutator.observer.project+"/"+mutator.observer.repositoryID {
		return fmt.Errorf("azure devops locator repository does not match configuration")
	}
	if locator.ExternalID == "" && !requireExternalID {
		return nil
	}
	if _, err := mutator.observer.validateLocator(locator); err != nil {
		return err
	}
	return nil
}

func (mutator *Mutator) endpoint(
	suffix string,
	query url.Values,
) *url.URL {
	target := *mutator.observer.organizationURL
	target.Path = mutator.observer.organizationURL.Path +
		"/" + mutator.observer.project +
		"/_apis/git/repositories/" + mutator.observer.repositoryID + suffix
	target.RawPath = ""
	canonical := cloneMutationQuery(query)
	canonical.Set("api-version", apiVersion)
	target.RawQuery = canonical.Encode()
	target.Fragment = ""
	target.User = nil
	return &target
}

func readMutationCollection[T any](
	ctx context.Context,
	mutator *Mutator,
	target *url.URL,
) ([]T, error) {
	var output []T
	seen := make(map[string]struct{})
	for page := 0; target != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("azure devops mutation collection exceeds pagination limit")
		}
		if _, duplicate := seen[target.String()]; duplicate {
			return nil, fmt.Errorf("azure devops mutation pagination repeats a page")
		}
		seen[target.String()] = struct{}{}
		var current collectionPage[T]
		continuation, err := mutator.doJSON(
			ctx, http.MethodGet, target, nil, &current,
		)
		if err != nil {
			return nil, err
		}
		if current.Count == nil || current.Value == nil ||
			*current.Count != len(*current.Value) ||
			len(output)+len(*current.Value) > maxMutationItems {
			return nil, fmt.Errorf("azure devops mutation collection is malformed")
		}
		output = append(output, (*current.Value)...)
		if continuation == "" {
			target = nil
			continue
		}
		next := *target
		query := next.Query()
		query.Set("continuationToken", continuation)
		next.RawQuery = query.Encode()
		target = &next
	}
	return output, nil
}

func (mutator *Mutator) doJSON(
	ctx context.Context,
	method string,
	target *url.URL,
	payload any,
	destination any,
) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("azure devops mutation context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := mutator.validateMutationURL(target); err != nil {
		return "", err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil || len(raw) > maxMutationRequestBytes {
			return "", fmt.Errorf("azure devops mutation request is invalid")
		}
		body = bytes.NewReader(raw)
	}
	credential, err := mutator.observer.token.Token(ctx)
	if err != nil || strings.TrimSpace(credential) == "" {
		return "", fmt.Errorf("azure devops bearer credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return "", fmt.Errorf("azure devops request construction failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("User-Agent", userAgent)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := mutator.observer.client.Do(request)
	if err != nil {
		return "", fmt.Errorf(
			"azure devops request failed: %s",
			redact(err.Error(), credential),
		)
	}
	defer response.Body.Close()
	if response.Request == nil ||
		mutator.validateMutationURL(response.Request.URL) != nil {
		return "", fmt.Errorf("azure devops response came from outside the configured organization")
	}
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return "", azureStatusError(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("azure devops response read failed")
	}
	if len(raw) > maxBodyBytes {
		return "", fmt.Errorf(
			"azure devops response exceeds %d bytes", maxBodyBytes,
		)
	}
	if destination != nil {
		if err := decodeJSON(raw, destination); err != nil {
			return "", fmt.Errorf("azure devops response is invalid JSON")
		}
	}
	return continuationToken(response.Header)
}

func (mutator *Mutator) doNonPaginatedJSON(
	ctx context.Context,
	method string,
	target *url.URL,
	payload any,
	destination any,
	operation string,
) error {
	continuation, err := mutator.doJSON(
		ctx, method, target, payload, destination,
	)
	if err != nil {
		return err
	}
	if continuation != "" {
		return fmt.Errorf("azure devops %s response unexpectedly paginated", operation)
	}
	return nil
}

func (mutator *Mutator) validateMutationURL(candidate *url.URL) error {
	if candidate == nil || candidate.User != nil || candidate.ForceQuery ||
		candidate.Fragment != "" ||
		candidate.Scheme != mutator.observer.organizationURL.Scheme ||
		!strings.EqualFold(
			candidate.Host, mutator.observer.organizationURL.Host,
		) {
		return fmt.Errorf("azure devops mutation URL is outside the configured organization")
	}
	prefix := mutator.observer.organizationURL.Path +
		"/" + mutator.observer.project +
		"/_apis/git/repositories/" + mutator.observer.repositoryID
	relative := strings.TrimPrefix(candidate.Path, prefix)
	validPath := relative == "/refs" ||
		relative == "/pullRequests" ||
		validMutationPullPath(relative)
	if !validPath {
		return fmt.Errorf("azure devops mutation URL is outside the configured repository")
	}
	query := candidate.Query()
	if values := query["api-version"]; len(values) != 1 ||
		values[0] != apiVersion {
		return fmt.Errorf("azure devops mutation URL does not pin REST 7.1")
	}
	for key, values := range query {
		switch key {
		case "api-version":
		case "filter":
			if len(values) != 1 ||
				validateBranchRef("refs/"+values[0]) != nil {
				return fmt.Errorf("azure devops ref filter is invalid")
			}
		case "continuationToken":
			if len(values) != 1 || validateContinuation(values[0]) != nil {
				return fmt.Errorf("azure devops continuation token is invalid")
			}
		case "searchCriteria.sourceRefName", "searchCriteria.targetRefName":
			if relative != "/pullRequests" || len(values) != 1 ||
				validateBranchRef(values[0]) != nil {
				return fmt.Errorf("azure devops pull request search ref is invalid")
			}
		case "searchCriteria.status":
			if relative != "/pullRequests" || len(values) != 1 ||
				values[0] != "all" {
				return fmt.Errorf("azure devops pull request search status is invalid")
			}
		default:
			return fmt.Errorf("azure devops mutation URL has unexpected query parameters")
		}
	}
	if candidate.RawQuery != query.Encode() {
		return fmt.Errorf("azure devops mutation URL query is not canonical")
	}
	return nil
}

func validMutationPullPath(relative string) bool {
	const prefix = "/pullRequests/"
	if !strings.HasPrefix(relative, prefix) {
		return false
	}
	raw := strings.TrimPrefix(relative, prefix)
	parts := strings.Split(raw, "/")
	if len(parts) != 1 && !(len(parts) == 2 &&
		(parts[1] == "iterations" ||
			parts[1] == "statuses" ||
			parts[1] == "threads")) &&
		!(len(parts) == 4 && parts[1] == "threads" &&
			parts[3] == "comments" &&
			canonicalPositiveInt32(parts[2])) {
		return false
	}
	return canonicalPositiveInt32(parts[0])
}

func canonicalPositiveInt32(raw string) bool {
	value, err := strconv.ParseInt(raw, 10, 32)
	return err == nil && value > 0 && strconv.FormatInt(value, 10) == raw
}

func operationMarker(kind, operationKey, subject string) string {
	suffix := ""
	if subject != "" {
		suffix = " " + subject
	}
	return "<!-- Jetbridge-Operation: " + kind + " " +
		operationKey + suffix + " -->"
}

func appendOperationMarker(body, marker string) string {
	return strings.TrimRight(body, "\n") + "\n\n" + marker
}

func exactOperationMarker(
	body string,
	expected string,
	operationKey string,
) (bool, error) {
	if !utf8.ValidString(body) || len(body) > 64<<10 {
		return false, fmt.Errorf("azure devops operation marker body is invalid")
	}
	matches := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == expected {
			matches++
			continue
		}
		if strings.Contains(line, mutationOperationMetadata) &&
			strings.Contains(line, operationKey) &&
			!mutationMarkerPattern.MatchString(line) {
			return false, fmt.Errorf("azure devops operation marker was altered")
		}
	}
	if matches > 1 {
		return false, fmt.Errorf("azure devops operation marker is ambiguous")
	}
	return matches == 1, nil
}

func boundedMutationText(value string, maximum int, allowNewline bool) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) ||
		len(value) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) &&
			!(allowNewline &&
				(character == '\n' || character == '\r' || character == '\t')) {
			return false
		}
	}
	return true
}

func validAbsoluteMutationURL(value string) bool {
	if value == "" || len(value) > 2048 {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil
}

func cloneMutationQuery(values url.Values) url.Values {
	cloned := make(url.Values, len(values)+1)
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

var (
	_ pullrequest.Observer = (*Adapter)(nil)
	_ pullrequest.Mutator  = (*Adapter)(nil)
	_ pullrequest.Mutator  = (*Mutator)(nil)
)
