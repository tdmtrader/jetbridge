package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type OperationKind string

const (
	OperationPublishPRBranch OperationKind = "publish_pr_branch"
	OperationCreatePR        OperationKind = "create_pr"
	OperationPublishPRStatus OperationKind = "publish_pr_status"
	OperationRespondToReview OperationKind = "respond_to_review"
)

type HeadExpectation = contracts.PullRequestHeadExpectation

type PRProvider string

const (
	PRProviderGitHub PRProvider = "github"
)

type PRLocator struct {
	Provider   PRProvider `json:"provider"`
	Repository string     `json:"repository"`
	ExternalID string     `json:"external_id,omitempty"`
}

type PRReviewBatch = contracts.PullRequestReviewBatch

type BranchPublicationRequest struct {
	Authority             Authority            `json:"authority"`
	Observation           snapshot.SnapshotRef `json:"observation"`
	Candidate             snapshot.SnapshotRef `json:"candidate"`
	Validation            snapshot.SnapshotRef `json:"validation"`
	Impact                snapshot.SnapshotRef `json:"impact"`
	Evidence              PublicationEvidence  `json:"evidence"`
	Destination           string               `json:"destination"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	Locator               PRLocator            `json:"locator"`
	SourceRef             string               `json:"source_ref"`
	TargetRef             string               `json:"target_ref"`
	ExpectedSource        HeadExpectation      `json:"expected_source"`
	ExpectedTargetSHA     string               `json:"expected_target_sha"`
	NewSourceSHA          string               `json:"new_source_sha"`
}

type PullRequestPublicationRequest struct {
	Authority             Authority            `json:"authority"`
	Observation           snapshot.SnapshotRef `json:"observation"`
	Candidate             snapshot.SnapshotRef `json:"candidate"`
	Validation            snapshot.SnapshotRef `json:"validation"`
	Impact                snapshot.SnapshotRef `json:"impact"`
	Evidence              PublicationEvidence  `json:"evidence"`
	Destination           string               `json:"destination"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	Locator               PRLocator            `json:"locator"`
	SourceRef             string               `json:"source_ref"`
	SourceSHA             string               `json:"source_sha"`
	TargetRef             string               `json:"target_ref"`
	TargetSHA             string               `json:"target_sha"`
	Title                 string               `json:"title"`
	Body                  string               `json:"body"`
}

type StatusPublicationRequest struct {
	Authority             Authority            `json:"authority"`
	Observation           snapshot.SnapshotRef `json:"observation"`
	Validation            snapshot.SnapshotRef `json:"validation"`
	Evidence              PublicationEvidence  `json:"evidence"`
	Destination           string               `json:"destination"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	Locator               PRLocator            `json:"locator"`
	TargetRef             string               `json:"target_ref"`
	SourceSHA             string               `json:"source_sha"`
	State                 string               `json:"state"`
	Description           string               `json:"description"`
	TargetURL             string               `json:"target_url"`
}

type ResponsePublicationRequest struct {
	Authority             Authority                         `json:"authority"`
	Observation           snapshot.SnapshotRef              `json:"observation"`
	ResponseSnapshot      snapshot.SnapshotRef              `json:"response_snapshot"`
	Evidence              PublicationEvidence               `json:"evidence"`
	Destination           string                            `json:"destination"`
	ApprovalPolicyVersion string                            `json:"approval_policy_version"`
	Locator               PRLocator                         `json:"locator"`
	TargetRef             string                            `json:"target_ref"`
	Batch                 PRReviewBatch                     `json:"batch"`
	Response              contracts.PullRequestResponseBody `json:"response"`
}

// PRAction is a closed union used by the migration-independent publication
// store seam. Exactly one request must match Kind.
type PRAction struct {
	Kind        OperationKind                  `json:"kind"`
	Branch      *BranchPublicationRequest      `json:"branch,omitempty"`
	PullRequest *PullRequestPublicationRequest `json:"pull_request,omitempty"`
	Status      *StatusPublicationRequest      `json:"status,omitempty"`
	Response    *ResponsePublicationRequest    `json:"response,omitempty"`
}

func (request BranchPublicationRequest) OperationKey() (string, error) {
	return branchAction(request).OperationKey()
}

func (request PullRequestPublicationRequest) OperationKey() (string, error) {
	return pullRequestAction(request).OperationKey()
}

func (request StatusPublicationRequest) OperationKey() (string, error) {
	return statusAction(request).OperationKey()
}

func (request ResponsePublicationRequest) OperationKey() (string, error) {
	return responseAction(request).OperationKey()
}

func (action PRAction) OperationKey() (string, error) {
	action = action.Clone()
	if err := action.Validate(); err != nil {
		return "", err
	}
	action.stripOccurrenceAuthority()
	raw, err := json.Marshal(action)
	if err != nil {
		return "", fmt.Errorf("%w: encode PR operation identity", ErrInvalidRequest)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (action PRAction) Validate() error {
	selected := 0
	for _, present := range []bool{action.Branch != nil, action.PullRequest != nil, action.Status != nil, action.Response != nil} {
		if present {
			selected++
		}
	}
	if selected != 1 {
		return fmt.Errorf("%w: PR action must select exactly one request", ErrInvalidRequest)
	}
	switch action.Kind {
	case OperationPublishPRBranch:
		if action.Branch == nil {
			return fmt.Errorf("%w: PR action kind does not match request", ErrInvalidRequest)
		}
		return action.Branch.Validate()
	case OperationCreatePR:
		if action.PullRequest == nil {
			return fmt.Errorf("%w: PR action kind does not match request", ErrInvalidRequest)
		}
		return action.PullRequest.Validate()
	case OperationPublishPRStatus:
		if action.Status == nil {
			return fmt.Errorf("%w: PR action kind does not match request", ErrInvalidRequest)
		}
		return action.Status.Validate()
	case OperationRespondToReview:
		if action.Response == nil {
			return fmt.Errorf("%w: PR action kind does not match request", ErrInvalidRequest)
		}
		return action.Response.Validate()
	default:
		return fmt.Errorf("%w: PR operation kind is invalid", ErrInvalidRequest)
	}
}

func (action PRAction) ValidatePersisted() error {
	if err := action.Validate(); err != nil {
		return err
	}
	return action.authority().validate(true)
}

func (action PRAction) Clone() PRAction {
	if action.Branch != nil {
		value := action.Branch.clone()
		action.Branch = &value
	}
	if action.PullRequest != nil {
		value := action.PullRequest.clone()
		action.PullRequest = &value
	}
	if action.Status != nil {
		value := action.Status.clone()
		action.Status = &value
	}
	if action.Response != nil {
		value := action.Response.clone()
		action.Response = &value
	}
	return action
}

func (request BranchPublicationRequest) Validate() error {
	if err := validatePRCommon(request.Authority, request.Evidence, request.Destination, request.ApprovalPolicyVersion); err != nil {
		return err
	}
	if err := validateSnapshotType("observation", request.Observation, "pull-request/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("candidate", request.Candidate, "repository-change/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("validation", request.Validation, "validation/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("impact", request.Impact, "publish-impact/v1"); err != nil {
		return err
	}
	if err := validatePRLocator(request.Locator, false, true); err != nil {
		return err
	}
	if err := validatePRBranchPair(request.SourceRef, request.TargetRef); err != nil {
		return err
	}
	if err := request.ExpectedSource.Validate(); err != nil {
		return fmt.Errorf("%w: expected source: %v", ErrInvalidRequest, err)
	}
	if !validObjectID(request.ExpectedTargetSHA) || !validObjectID(request.NewSourceSHA) {
		return fmt.Errorf("%w: branch object identity is invalid", ErrInvalidRequest)
	}
	if request.ExpectedSource.Exists && len(request.ExpectedSource.SHA) != len(request.NewSourceSHA) ||
		len(request.ExpectedTargetSHA) != len(request.NewSourceSHA) {
		return fmt.Errorf("%w: branch object formats do not match", ErrInvalidRequest)
	}
	return nil
}

func (request PullRequestPublicationRequest) Validate() error {
	if err := validatePRCommon(request.Authority, request.Evidence, request.Destination, request.ApprovalPolicyVersion); err != nil {
		return err
	}
	if err := validateSnapshotType("observation", request.Observation, "pull-request/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("candidate", request.Candidate, "repository-change/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("validation", request.Validation, "validation/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("impact", request.Impact, "publish-impact/v1"); err != nil {
		return err
	}
	if err := validatePRLocator(request.Locator, false, false); err != nil {
		return err
	}
	if err := validatePRBranchPair(request.SourceRef, request.TargetRef); err != nil {
		return err
	}
	if !validObjectID(request.SourceSHA) || !validObjectID(request.TargetSHA) || len(request.SourceSHA) != len(request.TargetSHA) {
		return fmt.Errorf("%w: pull request object identity is invalid", ErrInvalidRequest)
	}
	if !boundedText(request.Title, 256, false) || !boundedMarkdown(request.Body, 32<<10) {
		return fmt.Errorf("%w: pull request title or body is invalid", ErrInvalidRequest)
	}
	if strings.Contains(request.Body, "Jetbridge-Operation:") {
		return fmt.Errorf("%w: pull request body contains reserved operation metadata", ErrInvalidRequest)
	}
	return nil
}

func (request StatusPublicationRequest) Validate() error {
	if err := validatePRCommon(request.Authority, request.Evidence, request.Destination, request.ApprovalPolicyVersion); err != nil {
		return err
	}
	if err := validateSnapshotType("observation", request.Observation, "pull-request/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("validation", request.Validation, "validation/v1"); err != nil {
		return err
	}
	if err := validatePRLocator(request.Locator, true, false); err != nil {
		return err
	}
	if !safePRRef(request.TargetRef) {
		return fmt.Errorf("%w: status target ref is invalid", ErrInvalidRequest)
	}
	if !validObjectID(request.SourceSHA) {
		return fmt.Errorf("%w: status source sha is invalid", ErrInvalidRequest)
	}
	switch request.State {
	case "error", "failure", "pending", "success":
	default:
		return fmt.Errorf("%w: status state is invalid", ErrInvalidRequest)
	}
	if !boundedText(request.Description, 140, false) || !validHTTPURL(request.TargetURL, 2048) {
		return fmt.Errorf("%w: status description or target URL is invalid", ErrInvalidRequest)
	}
	return nil
}

func (request ResponsePublicationRequest) Validate() error {
	if err := validatePRCommon(request.Authority, request.Evidence, request.Destination, request.ApprovalPolicyVersion); err != nil {
		return err
	}
	if err := validateSnapshotType("observation", request.Observation, "pull-request/v1"); err != nil {
		return err
	}
	if err := validateSnapshotType("response", request.ResponseSnapshot, "pull-request-response/v1"); err != nil {
		return err
	}
	if err := validatePRLocator(request.Locator, true, false); err != nil {
		return err
	}
	if !safePRRef(request.TargetRef) {
		return fmt.Errorf("%w: response target ref is invalid", ErrInvalidRequest)
	}
	knownThreads := make(map[string]struct{}, len(request.Batch.ThreadIDs))
	for _, threadID := range request.Batch.ThreadIDs {
		knownThreads[threadID] = struct{}{}
	}
	if err := request.Batch.Validate(knownThreads); err != nil {
		return fmt.Errorf("%w: review batch: %v", ErrInvalidRequest, err)
	}
	if err := request.Response.Validate(nil); err != nil {
		return fmt.Errorf("%w: review response: %v", ErrInvalidRequest, err)
	}
	if request.Response.BatchID != request.Batch.ID {
		return fmt.Errorf("%w: response batch is not authorized", ErrInvalidRequest)
	}
	for _, reply := range request.Response.Replies {
		if _, found := knownThreads[reply.ThreadID]; !found {
			return fmt.Errorf("%w: response thread is not authorized", ErrInvalidRequest)
		}
		if strings.Contains(reply.Body, "Jetbridge-Operation:") {
			return fmt.Errorf("%w: response contains reserved operation metadata", ErrInvalidRequest)
		}
	}
	if strings.Contains(request.Response.Summary, "Jetbridge-Operation:") {
		return fmt.Errorf("%w: response contains reserved operation metadata", ErrInvalidRequest)
	}
	return nil
}

func branchAction(request BranchPublicationRequest) PRAction {
	value := request.clone()
	return PRAction{Kind: OperationPublishPRBranch, Branch: &value}
}

func pullRequestAction(request PullRequestPublicationRequest) PRAction {
	value := request.clone()
	return PRAction{Kind: OperationCreatePR, PullRequest: &value}
}

func statusAction(request StatusPublicationRequest) PRAction {
	value := request.clone()
	return PRAction{Kind: OperationPublishPRStatus, Status: &value}
}

func responseAction(request ResponsePublicationRequest) PRAction {
	value := request.clone()
	return PRAction{Kind: OperationRespondToReview, Response: &value}
}

func (action PRAction) authority() Authority {
	switch action.Kind {
	case OperationPublishPRBranch:
		return action.Branch.Authority
	case OperationCreatePR:
		return action.PullRequest.Authority
	case OperationPublishPRStatus:
		return action.Status.Authority
	case OperationRespondToReview:
		return action.Response.Authority
	default:
		return Authority{}
	}
}

func (action *PRAction) stripOccurrenceAuthority() {
	authority := Authority{TeamID: action.authority().TeamID}
	switch action.Kind {
	case OperationPublishPRBranch:
		action.Branch.Authority = authority
	case OperationCreatePR:
		action.PullRequest.Authority = authority
	case OperationPublishPRStatus:
		action.Status.Authority = authority
	case OperationRespondToReview:
		action.Response.Authority = authority
	}
}

func (request BranchPublicationRequest) clone() BranchPublicationRequest {
	request.Evidence = request.Evidence.Clone()
	return request
}

func (request PullRequestPublicationRequest) clone() PullRequestPublicationRequest {
	request.Evidence = request.Evidence.Clone()
	return request
}

func (request StatusPublicationRequest) clone() StatusPublicationRequest {
	request.Evidence = request.Evidence.Clone()
	return request
}

func (request ResponsePublicationRequest) clone() ResponsePublicationRequest {
	request.Evidence = request.Evidence.Clone()
	request.Batch.ThreadIDs = append([]string(nil), request.Batch.ThreadIDs...)
	request.Response.Replies = append([]contracts.PullRequestThreadResponse(nil), request.Response.Replies...)
	return request
}

func validatePRCommon(authority Authority, evidence PublicationEvidence, destination, policyVersion string) error {
	if err := authority.validate(false); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if !boundedText(destination, 2048, false) || !boundedText(policyVersion, 128, false) {
		return fmt.Errorf("%w: PR destination or approval policy is invalid", ErrInvalidRequest)
	}
	return nil
}

func validateSnapshotType(label string, reference snapshot.SnapshotRef, expected snapshot.TypeRef) error {
	if reference.Validate() != nil || reference.Type != expected {
		return fmt.Errorf("%w: %s must be an exact %s snapshot", ErrInvalidRequest, label, expected)
	}
	return nil
}

func validatePRLocator(locator PRLocator, requireExternalID, allowExternalID bool) error {
	switch locator.Provider {
	case PRProviderGitHub:
	default:
		return fmt.Errorf("%w: PR provider is invalid", ErrInvalidRequest)
	}
	if !boundedText(locator.Repository, 512, false) || strings.ContainsAny(locator.Repository, " \t\r\n") {
		return fmt.Errorf("%w: PR repository is invalid", ErrInvalidRequest)
	}
	if requireExternalID {
		if !boundedText(locator.ExternalID, 256, false) {
			return fmt.Errorf("%w: PR external id is invalid", ErrInvalidRequest)
		}
	} else if locator.ExternalID != "" && !allowExternalID {
		return fmt.Errorf("%w: pre-create PR locator must not carry an external id", ErrInvalidRequest)
	} else if locator.ExternalID != "" && !boundedText(locator.ExternalID, 256, false) {
		return fmt.Errorf("%w: PR external id is invalid", ErrInvalidRequest)
	}
	return nil
}

func validatePRBranchPair(source, target string) error {
	if !safePRRef(source) || !safePRRef(target) || source == target {
		return fmt.Errorf("%w: PR source and target refs are invalid", ErrInvalidRequest)
	}
	return nil
}

func safePRRef(reference string) bool {
	if !strings.HasPrefix(reference, "refs/heads/") || len(reference) > 512 ||
		strings.ContainsAny(reference, " \t\r\n\x00\\~^:?*[") ||
		strings.Contains(reference, "..") || strings.Contains(reference, "@{") ||
		strings.HasSuffix(reference, ".") || strings.HasSuffix(reference, ".lock") {
		return false
	}
	for _, part := range strings.Split(reference, "/") {
		if part == "" || part == "." || part == "@" || strings.HasPrefix(part, ".") ||
			strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func boundedMarkdown(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.IndexByte(value, 0) < 0
}

func validHTTPURL(value string, maximum int) bool {
	if !boundedText(value, maximum, false) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}
