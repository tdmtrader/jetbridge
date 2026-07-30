package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pullrequest"
)

const (
	operationMetadata       = "Jetbridge-Operation:"
	maxMutationCollection   = 1024
	maxMutationRequestBytes = 64 << 10
)

var (
	operationKeyPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	machineMarkerPattern = regexp.MustCompile(
		`^<!-- Jetbridge-Operation: (create_pr sha256:[0-9a-f]{64}|respond_to_review sha256:[0-9a-f]{64} (summary|thread thread-[1-9][0-9]*)) -->$`,
	)
)

type BranchWriter interface {
	CompareAndSwapBranch(context.Context, pullrequest.BranchMutation) (pullrequest.BranchResult, error)
}

// Mutator implements only provider-native pull-request side effects. Merge
// and completion remain forge-native and are intentionally absent.
type Mutator struct {
	observer *Observer
	branches BranchWriter
}

func NewMutator(
	baseURL string,
	token pullrequest.TokenSource,
	branches BranchWriter,
	client *http.Client,
) (*Mutator, error) {
	if nilMutationInterface(branches) {
		return nil, fmt.Errorf("github branch writer is required")
	}
	observer, err := NewObserver(baseURL, token, client)
	if err != nil {
		return nil, err
	}
	return &Mutator{observer: observer, branches: branches}, nil
}

func (mutator *Mutator) CompareAndSwapBranch(
	ctx context.Context,
	mutation pullrequest.BranchMutation,
) (pullrequest.BranchResult, error) {
	if mutator == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("github mutator is required")
	}
	if err := validateBranchMutation(mutation); err != nil {
		return pullrequest.BranchResult{}, err
	}
	result, err := mutator.branches.CompareAndSwapBranch(ctx, mutation)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	if result.HeadSHA != mutation.NewSourceSHA {
		return pullrequest.BranchResult{}, fmt.Errorf("github branch writer returned the wrong source head")
	}
	return result, nil
}

func (mutator *Mutator) FindOrCreatePullRequest(
	ctx context.Context,
	request pullrequest.CreateRequest,
) (pullrequest.ExternalPullRequest, error) {
	if mutator == nil {
		return pullrequest.ExternalPullRequest{}, fmt.Errorf("github mutator is required")
	}
	if err := validateCreateRequest(request); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	marker := operationMarker("create_pr", request.OperationKey, "")
	matches, err := mutator.findPullRequests(ctx, request, marker)
	if err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	switch len(matches) {
	case 1:
		return mutator.recoverPullRequest(ctx, request, matches[0], marker)
	case 0:
	default:
		return pullrequest.ExternalPullRequest{}, fmt.Errorf("github pull request operation marker is ambiguous")
	}

	target := mutator.observer.endpoint("/repos/" + request.Locator.Repository + "/pulls")
	var created mutationPull
	if _, err := mutator.doJSON(ctx, http.MethodPost, target, struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{
		Title: request.Title,
		Head:  strings.TrimPrefix(request.SourceRef, "refs/heads/"),
		Base:  strings.TrimPrefix(request.TargetRef, "refs/heads/"),
		Body:  appendMarker(request.Body, marker),
	}, &created); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	if err := validateCreatedPull(request, created, marker); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	return externalPullRequest(mutator.observer, request, created)
}

func (mutator *Mutator) recoverPullRequest(
	ctx context.Context,
	request pullrequest.CreateRequest,
	match mutationPull,
	marker string,
) (pullrequest.ExternalPullRequest, error) {
	if match.Number <= 0 {
		return pullrequest.ExternalPullRequest{}, fmt.Errorf("github matching pull request number is invalid")
	}
	locator := request.Locator
	locator.ExternalID = strconv.FormatInt(match.Number, 10)
	detailed, err := mutator.getPull(ctx, locator)
	if err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	sourceRef, sourceErr := normalizeBranchRef(detailed.Head.Ref)
	targetRef, targetErr := normalizeBranchRef(detailed.Base.Ref)
	exact, markerErr := exactMarker(detailed.Body, marker, request.OperationKey)
	if sourceErr != nil || targetErr != nil || markerErr != nil ||
		!exact || sourceRef != request.SourceRef || targetRef != request.TargetRef {
		return pullrequest.ExternalPullRequest{}, fmt.Errorf("github recovered pull request does not match its exact operation")
	}
	return externalPullRequest(mutator.observer, request, detailed)
}

func (mutator *Mutator) PublishValidationStatus(
	ctx context.Context,
	request pullrequest.StatusRequest,
) (pullrequest.ExternalResult, error) {
	if mutator == nil {
		return pullrequest.ExternalResult{}, fmt.Errorf("github mutator is required")
	}
	if err := validateStatusRequest(request); err != nil {
		return pullrequest.ExternalResult{}, err
	}
	contextName := "jetbridge/" + request.OperationKey
	statuses, err := mutator.statuses(ctx, request)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	var recovered *commitStatus
	for index := range statuses {
		status := statuses[index]
		if status.Context != contextName {
			continue
		}
		if !status.matches(request) {
			return pullrequest.ExternalResult{}, fmt.Errorf("github status operation context was altered")
		}
		if recovered == nil {
			value := status
			recovered = &value
		}
	}
	if recovered != nil {
		return externalStatus(mutator.observer, request.OperationKey, *recovered)
	}

	// Recovery is intentionally checked first: the exact status may have been
	// accepted before a lost response even if the pull request has since
	// advanced. A new write, however, requires the exact current source head.
	providerPull, err := mutator.getPull(ctx, request.Locator)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if providerPull.Head.SHA != request.SourceSHA {
		return pullrequest.ExternalResult{}, fmt.Errorf("github status source head is stale")
	}

	target := mutator.observer.endpoint(
		"/repos/" + request.Locator.Repository + "/statuses/" + request.SourceSHA,
	)
	var created commitStatus
	if _, err := mutator.doJSON(ctx, http.MethodPost, target, struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}{
		State: request.State, TargetURL: request.TargetURL,
		Description: request.Description, Context: contextName,
	}, &created); err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if created.Context != contextName || !created.matches(request) {
		return pullrequest.ExternalResult{}, fmt.Errorf("github created status does not match the operation")
	}
	return externalStatus(mutator.observer, request.OperationKey, created)
}

func (mutator *Mutator) PublishReviewResponse(
	ctx context.Context,
	request pullrequest.ResponseRequest,
) (pullrequest.ExternalResult, error) {
	if mutator == nil {
		return pullrequest.ExternalResult{}, fmt.Errorf("github mutator is required")
	}
	threadRoots, err := validateResponseRequest(request)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	providerPull, err := mutator.getPull(ctx, request.Locator)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	issueComments, err := mutator.issueComments(ctx, request.Locator)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	reviewComments, err := mutator.reviewComments(ctx, request.Locator)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}

	summaryMarker := operationMarker("respond_to_review", request.OperationKey, "summary")
	found, err := recoverCommentMarker(issueComments, summaryMarker, request.OperationKey)
	if err != nil {
		return pullrequest.ExternalResult{}, err
	}
	if !found {
		target := mutator.observer.endpoint(
			"/repos/" + request.Locator.Repository + "/issues/" +
				request.Locator.ExternalID + "/comments",
		)
		if _, err := mutator.createComment(
			ctx, target, appendMarker(request.Response.Summary, summaryMarker),
		); err != nil {
			return pullrequest.ExternalResult{}, err
		}
	}

	for _, reply := range request.Response.Replies {
		marker := operationMarker(
			"respond_to_review", request.OperationKey, "thread "+reply.ThreadID,
		)
		found, err := recoverCommentMarker(reviewComments, marker, request.OperationKey)
		if err != nil {
			return pullrequest.ExternalResult{}, err
		}
		if found {
			continue
		}
		target := mutator.observer.endpoint(
			"/repos/" + request.Locator.Repository + "/pulls/" +
				request.Locator.ExternalID + "/comments/" +
				strconv.FormatInt(threadRoots[reply.ThreadID], 10) + "/replies",
		)
		if _, err := mutator.createComment(
			ctx, target, appendMarker(reply.Body, marker),
		); err != nil {
			return pullrequest.ExternalResult{}, err
		}
	}
	return pullrequest.ExternalResult{
		OperationKey: request.OperationKey,
		ExternalID:   request.Batch.ID,
		URL:          providerPull.HTMLURL,
	}, nil
}

type mutationPull struct {
	Number  int64    `json:"number"`
	HTMLURL string   `json:"html_url"`
	State   string   `json:"state"`
	Merged  bool     `json:"merged"`
	Body    string   `json:"body"`
	Head    pullHead `json:"head"`
	Base    pullBase `json:"base"`
}

type commitStatus struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
	Context     string `json:"context"`
}

func (status commitStatus) matches(request pullrequest.StatusRequest) bool {
	return status.State == request.State &&
		status.Description == request.Description &&
		status.TargetURL == request.TargetURL
}

type providerComment struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

func (mutator *Mutator) findPullRequests(
	ctx context.Context,
	request pullrequest.CreateRequest,
	marker string,
) ([]mutationPull, error) {
	sourceBranch := strings.TrimPrefix(request.SourceRef, "refs/heads/")
	targetBranch := strings.TrimPrefix(request.TargetRef, "refs/heads/")
	owner := strings.Split(request.Locator.Repository, "/")[0]
	target := mutator.observer.endpoint(
		"/repos/" + request.Locator.Repository + "/pulls",
	)
	staticQuery := url.Values{
		"base":     []string{targetBranch},
		"head":     []string{owner + ":" + sourceBranch},
		"per_page": []string{"100"},
		"state":    []string{"all"},
	}
	target.RawQuery = staticQuery.Encode()
	all, err := readMutationCollection[mutationPull](
		ctx, mutator, target, staticQuery,
	)
	if err != nil {
		return nil, err
	}
	matches := make([]mutationPull, 0, 1)
	for _, candidate := range all {
		candidateSource, sourceErr := normalizeBranchRef(candidate.Head.Ref)
		candidateTarget, targetErr := normalizeBranchRef(candidate.Base.Ref)
		if sourceErr != nil || targetErr != nil ||
			candidate.Head.Repository == nil ||
			candidate.Base.Repository == nil {
			return nil, fmt.Errorf("github pull request search returned an invalid pull request")
		}
		if candidateSource != request.SourceRef ||
			candidateTarget != request.TargetRef ||
			candidate.Head.Repository.FullName != request.Locator.Repository ||
			candidate.Base.Repository.FullName != request.Locator.Repository {
			continue
		}
		exact, markerErr := exactMarker(candidate.Body, marker, request.OperationKey)
		if markerErr != nil {
			return nil, markerErr
		}
		if !exact {
			return nil, fmt.Errorf("github matching pull request is missing its exact operation marker")
		}
		matches = append(matches, candidate)
	}
	return matches, nil
}

func (mutator *Mutator) statuses(
	ctx context.Context,
	request pullrequest.StatusRequest,
) ([]commitStatus, error) {
	target := mutator.observer.endpoint(
		"/repos/" + request.Locator.Repository + "/commits/" +
			request.SourceSHA + "/statuses",
	)
	staticQuery := url.Values{"per_page": []string{"100"}}
	target.RawQuery = staticQuery.Encode()
	statuses, err := readMutationCollection[commitStatus](
		ctx, mutator, target, staticQuery,
	)
	if err != nil {
		return nil, err
	}
	return statuses, nil
}

func (mutator *Mutator) issueComments(
	ctx context.Context,
	locator pullrequest.Locator,
) ([]providerComment, error) {
	target := mutator.observer.endpoint(
		"/repos/" + locator.Repository + "/issues/" + locator.ExternalID + "/comments",
	)
	return mutator.comments(ctx, target)
}

func (mutator *Mutator) reviewComments(
	ctx context.Context,
	locator pullrequest.Locator,
) ([]providerComment, error) {
	target := mutator.observer.endpoint(
		"/repos/" + locator.Repository + "/pulls/" + locator.ExternalID + "/comments",
	)
	return mutator.comments(ctx, target)
}

func (mutator *Mutator) comments(
	ctx context.Context,
	target *url.URL,
) ([]providerComment, error) {
	staticQuery := url.Values{"per_page": []string{"100"}}
	target.RawQuery = staticQuery.Encode()
	comments, err := readMutationCollection[providerComment](
		ctx, mutator, target, staticQuery,
	)
	if err != nil {
		return nil, err
	}
	return comments, nil
}

func (mutator *Mutator) getPull(
	ctx context.Context,
	locator pullrequest.Locator,
) (mutationPull, error) {
	target := mutator.observer.endpoint(
		"/repos/" + locator.Repository + "/pulls/" + locator.ExternalID,
	)
	var providerPull mutationPull
	if _, err := mutator.doJSON(ctx, http.MethodGet, target, nil, &providerPull); err != nil {
		return mutationPull{}, err
	}
	if err := validateProviderPull(mutator.observer, locator, providerPull); err != nil {
		return mutationPull{}, err
	}
	return providerPull, nil
}

func (mutator *Mutator) createComment(
	ctx context.Context,
	target *url.URL,
	body string,
) (providerComment, error) {
	var created providerComment
	if _, err := mutator.doJSON(
		ctx, http.MethodPost, target,
		struct {
			Body string `json:"body"`
		}{Body: body},
		&created,
	); err != nil {
		return providerComment{}, err
	}
	if created.ID <= 0 || created.Body != body || !validAbsoluteURL(created.HTMLURL) {
		return providerComment{}, fmt.Errorf("github created comment does not match the operation")
	}
	return created, nil
}

func readMutationCollection[T any](
	ctx context.Context,
	mutator *Mutator,
	target *url.URL,
	staticQuery url.Values,
) ([]T, error) {
	var destination []T
	seen := make(map[string]struct{})
	for page := 0; target != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("github mutation collection exceeds pagination limit")
		}
		if _, duplicate := seen[target.String()]; duplicate {
			return nil, fmt.Errorf("github mutation pagination repeats a page")
		}
		seen[target.String()] = struct{}{}
		var pageValue []T
		header, err := mutator.doJSON(
			ctx, http.MethodGet, target, nil, &pageValue,
		)
		if err != nil {
			return nil, err
		}
		if len(destination)+len(pageValue) > maxMutationCollection {
			return nil, fmt.Errorf("github mutation collection exceeds item limit")
		}
		destination = append(destination, pageValue...)
		target, err = mutator.nextMutationURL(
			header.Get("Link"), target.Path, staticQuery,
		)
		if err != nil {
			return nil, err
		}
	}
	return destination, nil
}

func (mutator *Mutator) doJSON(
	ctx context.Context,
	method string,
	target *url.URL,
	payload any,
	destination any,
) (http.Header, error) {
	if ctx == nil {
		return nil, fmt.Errorf("github mutation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := mutator.observer.validateURL(target); err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil || len(raw) > maxMutationRequestBytes {
			return nil, fmt.Errorf("github mutation request is invalid")
		}
		body = bytes.NewReader(raw)
	}
	token, err := mutator.observer.token.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("github credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("github request construction failed")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := mutator.observer.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %s", redact(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode > http.StatusMultipleChoices-1 {
		return nil, githubStatusError(response, token)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("github response read failed")
	}
	if len(raw) > maxBodyBytes {
		return nil, fmt.Errorf("github response exceeds %d bytes", maxBodyBytes)
	}
	if destination != nil {
		if err := decodeJSON(raw, destination); err != nil {
			return nil, fmt.Errorf("github response is invalid JSON")
		}
	}
	return response.Header.Clone(), nil
}

func (mutator *Mutator) nextMutationURL(
	link string,
	endpointPath string,
	staticQuery url.Values,
) (*url.URL, error) {
	if link == "" {
		return nil, nil
	}
	for _, component := range strings.Split(link, ",") {
		if !strings.Contains(component, `rel="next"`) {
			continue
		}
		start, end := strings.IndexByte(component, '<'), strings.IndexByte(component, '>')
		if start < 0 || end <= start+1 {
			return nil, fmt.Errorf("github mutation pagination link is malformed")
		}
		next, err := url.Parse(strings.TrimSpace(component[start+1 : end]))
		if err != nil || mutator.observer.validateURL(next) != nil ||
			next.Path != endpointPath {
			return nil, fmt.Errorf("github mutation pagination link changes endpoint or origin")
		}
		query := cloneValues(staticQuery)
		rawPage := next.Query().Get("page")
		page, err := strconv.ParseInt(rawPage, 10, 64)
		if err != nil || page <= 0 || strconv.FormatInt(page, 10) != rawPage {
			return nil, fmt.Errorf("github mutation pagination page is invalid")
		}
		query.Set("page", rawPage)
		if next.RawQuery != query.Encode() {
			return nil, fmt.Errorf("github mutation pagination query is invalid")
		}
		return next, nil
	}
	return nil, nil
}

func validateBranchMutation(mutation pullrequest.BranchMutation) error {
	if err := validateMutationLocator(mutation.Locator, false); err != nil {
		return err
	}
	if !validHeadRef(mutation.Ref) || !validHeadRef(mutation.TargetRef) ||
		mutation.Ref == mutation.TargetRef {
		return fmt.Errorf("github branch refs are invalid")
	}
	if err := mutation.ExpectedSource.Validate(); err != nil {
		return fmt.Errorf("github expected source is invalid: %w", err)
	}
	if !validObjectIDExact(mutation.ExpectedTargetSHA) ||
		!validObjectIDExact(mutation.NewSourceSHA) ||
		len(mutation.ExpectedTargetSHA) != len(mutation.NewSourceSHA) ||
		mutation.ExpectedSource.Exists &&
			len(mutation.ExpectedSource.SHA) != len(mutation.NewSourceSHA) ||
		!operationKeyPattern.MatchString(mutation.OperationKey) {
		return fmt.Errorf("github branch mutation identities are invalid")
	}
	return nil
}

func validateCreateRequest(request pullrequest.CreateRequest) error {
	if err := validateMutationLocator(request.Locator, false); err != nil {
		return err
	}
	if request.Locator.ExternalID != "" ||
		!validHeadRef(request.SourceRef) || !validHeadRef(request.TargetRef) ||
		request.SourceRef == request.TargetRef ||
		!validObjectIDExact(request.SourceSHA) ||
		!validObjectIDExact(request.TargetSHA) ||
		len(request.SourceSHA) != len(request.TargetSHA) ||
		!operationKeyPattern.MatchString(request.OperationKey) {
		return fmt.Errorf("github pull request creation identity is invalid")
	}
	if !boundedMutationText(request.Title, 256, false) ||
		!boundedMutationText(request.Body, 32<<10, true) ||
		strings.Contains(request.Body, operationMetadata) {
		return fmt.Errorf("github pull request metadata is invalid")
	}
	return nil
}

func validateStatusRequest(request pullrequest.StatusRequest) error {
	if err := validateMutationLocator(request.Locator, true); err != nil {
		return err
	}
	if !validObjectIDExact(request.SourceSHA) ||
		!operationKeyPattern.MatchString(request.OperationKey) {
		return fmt.Errorf("github status operation identity is invalid")
	}
	switch request.State {
	case "error", "failure", "pending", "success":
	default:
		return fmt.Errorf("github status state is invalid")
	}
	if !boundedMutationText(request.Description, 140, false) ||
		!validAbsoluteURL(request.TargetURL) {
		return fmt.Errorf("github status metadata is invalid")
	}
	return nil
}

func validateResponseRequest(
	request pullrequest.ResponseRequest,
) (map[string]int64, error) {
	if err := validateMutationLocator(request.Locator, true); err != nil {
		return nil, err
	}
	if !operationKeyPattern.MatchString(request.OperationKey) {
		return nil, fmt.Errorf("github response operation key is invalid")
	}
	known := make(map[string]struct{}, len(request.Batch.ThreadIDs))
	for _, threadID := range request.Batch.ThreadIDs {
		known[threadID] = struct{}{}
	}
	if err := request.Batch.Validate(known); err != nil {
		return nil, fmt.Errorf("github review batch is invalid: %w", err)
	}
	if err := request.Response.Validate(nil); err != nil {
		return nil, fmt.Errorf("github review response is invalid: %w", err)
	}
	if request.Response.BatchID != request.Batch.ID ||
		strings.Contains(request.Response.Summary, operationMetadata) {
		return nil, fmt.Errorf("github review response is not authorized by the batch")
	}
	roots := make(map[string]int64, len(request.Response.Replies))
	for _, reply := range request.Response.Replies {
		if _, authorized := known[reply.ThreadID]; !authorized ||
			strings.Contains(reply.Body, operationMetadata) {
			return nil, fmt.Errorf("github review response thread is not authorized")
		}
		root, err := parseThreadRoot(reply.ThreadID)
		if err != nil {
			return nil, fmt.Errorf("github review response thread is not authorized")
		}
		roots[reply.ThreadID] = root
	}
	return roots, nil
}

func validateMutationLocator(locator pullrequest.Locator, requireExternalID bool) error {
	if locator.Provider != pullrequest.ProviderGitHub {
		return fmt.Errorf("github mutator requires a github locator")
	}
	parts := strings.Split(locator.Repository, "/")
	if len(parts) != 2 ||
		!boundedMutationText(parts[0], 256, false) ||
		!boundedMutationText(parts[1], 256, false) ||
		strings.ContainsAny(locator.Repository, "\x00\r\n \t") {
		return fmt.Errorf("github repository must be canonical owner/repository")
	}
	if locator.ExternalID == "" && !requireExternalID {
		return nil
	}
	number, err := strconv.ParseInt(locator.ExternalID, 10, 64)
	if err != nil || number <= 0 ||
		strconv.FormatInt(number, 10) != locator.ExternalID {
		return fmt.Errorf("github pull request number must be a canonical positive integer")
	}
	return nil
}

func validateProviderPull(
	observer *Observer,
	locator pullrequest.Locator,
	value mutationPull,
) error {
	number, _ := strconv.ParseInt(locator.ExternalID, 10, 64)
	if value.Number != number ||
		value.Head.Repository == nil ||
		value.Head.Repository.FullName != locator.Repository ||
		value.Base.Repository == nil ||
		value.Base.Repository.FullName != locator.Repository ||
		!validObjectIDExact(value.Head.SHA) ||
		!validObjectIDExact(value.Base.SHA) {
		return fmt.Errorf("github pull request does not match its requested identity")
	}
	if _, err := normalizeBranchRef(value.Head.Ref); err != nil {
		return fmt.Errorf("github pull request source ref is invalid")
	}
	if _, err := normalizeBranchRef(value.Base.Ref); err != nil {
		return fmt.Errorf("github pull request target ref is invalid")
	}
	if _, err := normalizeState(pull{
		Number: value.Number, HTMLURL: value.HTMLURL, State: value.State,
		Merged: value.Merged, Head: value.Head, Base: value.Base,
	}); err != nil {
		return fmt.Errorf("github pull request state is invalid")
	}
	return observer.validateHTMLURL(locator, number, value.HTMLURL)
}

func validateCreatedPull(
	request pullrequest.CreateRequest,
	value mutationPull,
	marker string,
) error {
	locator := request.Locator
	locator.ExternalID = strconv.FormatInt(value.Number, 10)
	if value.Number <= 0 ||
		validateProviderPullForCreate(locator, value) != nil {
		return fmt.Errorf("github created pull request identity is invalid")
	}
	sourceRef, _ := normalizeBranchRef(value.Head.Ref)
	targetRef, _ := normalizeBranchRef(value.Base.Ref)
	exact, err := exactMarker(value.Body, marker, request.OperationKey)
	if err != nil || !exact ||
		sourceRef != request.SourceRef || value.Head.SHA != request.SourceSHA ||
		targetRef != request.TargetRef || value.Base.SHA != request.TargetSHA {
		return fmt.Errorf("github created pull request does not match the operation")
	}
	return nil
}

func validateProviderPullForCreate(
	locator pullrequest.Locator,
	value mutationPull,
) error {
	if value.Number <= 0 ||
		value.Head.Repository == nil ||
		value.Head.Repository.FullName != locator.Repository ||
		value.Base.Repository == nil ||
		value.Base.Repository.FullName != locator.Repository ||
		!validObjectIDExact(value.Head.SHA) ||
		!validObjectIDExact(value.Base.SHA) ||
		!validAbsoluteURL(value.HTMLURL) {
		return fmt.Errorf("provider pull request is invalid")
	}
	if _, err := normalizeBranchRef(value.Head.Ref); err != nil {
		return err
	}
	if _, err := normalizeBranchRef(value.Base.Ref); err != nil {
		return err
	}
	return nil
}

func externalPullRequest(
	observer *Observer,
	request pullrequest.CreateRequest,
	value mutationPull,
) (pullrequest.ExternalPullRequest, error) {
	externalID := strconv.FormatInt(value.Number, 10)
	locator := request.Locator
	locator.ExternalID = externalID
	if err := validateProviderPull(observer, locator, value); err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	state, err := normalizeState(pull{
		Number: value.Number, HTMLURL: value.HTMLURL, State: value.State,
		Merged: value.Merged, Head: value.Head, Base: value.Base,
	})
	if err != nil {
		return pullrequest.ExternalPullRequest{}, err
	}
	return pullrequest.ExternalPullRequest{
		Locator: locator, URL: value.HTMLURL, State: state,
		SourceRef: request.SourceRef, SourceSHA: request.SourceSHA,
		TargetRef: request.TargetRef, TargetSHA: request.TargetSHA,
	}, nil
}

func externalStatus(
	observer *Observer,
	operationKey string,
	status commitStatus,
) (pullrequest.ExternalResult, error) {
	statusURL, err := url.Parse(status.URL)
	if status.ID <= 0 || err != nil || observer.validateURL(statusURL) != nil {
		return pullrequest.ExternalResult{}, fmt.Errorf("github status result is invalid")
	}
	return pullrequest.ExternalResult{
		OperationKey: operationKey,
		ExternalID:   strconv.FormatInt(status.ID, 10),
		URL:          status.URL,
	}, nil
}

func recoverCommentMarker(
	comments []providerComment,
	marker string,
	operationKey string,
) (bool, error) {
	matches := 0
	for _, comment := range comments {
		if comment.ID <= 0 || !utf8.ValidString(comment.Body) ||
			len(comment.Body) > 32<<10 {
			return false, fmt.Errorf("github recovery comment is invalid")
		}
		exact, err := exactMarker(comment.Body, marker, operationKey)
		if err != nil {
			return false, err
		}
		if exact {
			matches++
		}
	}
	switch matches {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("github response operation marker is ambiguous")
	}
}

func exactMarker(body, marker, operationKey string) (bool, error) {
	matches := 0
	for _, line := range strings.Split(body, "\n") {
		switch {
		case line == marker:
			matches++
		case strings.Contains(line, operationMetadata) &&
			strings.Contains(line, operationKey):
			if !machineMarkerPattern.MatchString(line) {
				return false, fmt.Errorf("github operation marker was altered")
			}
		}
	}
	if matches > 1 {
		return false, fmt.Errorf("github operation marker is ambiguous")
	}
	return matches == 1, nil
}

func operationMarker(kind, operationKey, subject string) string {
	suffix := ""
	if subject != "" {
		suffix = " " + subject
	}
	return "<!-- Jetbridge-Operation: " + kind + " " + operationKey + suffix + " -->"
}

func appendMarker(body, marker string) string {
	return strings.TrimRight(body, "\n") + "\n\n" + marker
}

func parseThreadRoot(threadID string) (int64, error) {
	raw := strings.TrimPrefix(threadID, "thread-")
	if raw == threadID {
		return 0, fmt.Errorf("not a GitHub review thread")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf("not a canonical GitHub review thread")
	}
	return value, nil
}

func validHeadRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") {
		return false
	}
	_, err := normalizeBranchRef(strings.TrimPrefix(value, "refs/heads/"))
	return err == nil && len(value) <= 512
}

func validObjectIDExact(value string) bool {
	return validateObjectID(value) == nil
}

func boundedMutationText(value string, maximum int, allowNewline bool) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) ||
		len(value) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) &&
			!(allowNewline && (character == '\n' || character == '\r' || character == '\t')) {
			return false
		}
	}
	return true
}

func validAbsoluteURL(value string) bool {
	if len(value) == 0 || len(value) > 2048 {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

func nilMutationInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ pullrequest.Mutator = (*Mutator)(nil)
