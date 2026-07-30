package publisher

import (
	"context"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// PRImpactVerificationRequest is the complete immutable authority presented
// to the server-owned impact verifier. The sealed body is data, not authority:
// a verifier must recompute its deterministic inputs under PolicyVersion and
// compare the complete decision before returning it.
type PRImpactVerificationRequest struct {
	TeamID             int
	BindingID          int64
	ActionDigest       snapshot.Digest
	PolicyVersion      string
	Observation        snapshot.SnapshotRef
	Baseline           snapshot.SnapshotRef
	BaselineValidation snapshot.SnapshotRef
	Candidate          snapshot.SnapshotRef
	Validation         snapshot.SnapshotRef
	Impact             snapshot.SnapshotRef
	Response           snapshot.SnapshotRef
	AcceptedReview     PublicationEvidence
	Body               contracts.PublishImpactBody
}

func (request PRImpactVerificationRequest) Validate() error {
	if request.TeamID <= 0 ||
		request.BindingID <= 0 ||
		request.ActionDigest.Validate() != nil ||
		strings.TrimSpace(request.PolicyVersion) != request.PolicyVersion ||
		request.PolicyVersion == "" ||
		len(request.PolicyVersion) > 128 ||
		request.Observation.Validate() != nil ||
		request.Observation.Type != snapshot.TypeRef("pull-request/v1") ||
		request.Baseline.Validate() != nil ||
		request.Baseline.Type != snapshot.TypeRef("repository/v1") ||
		request.BaselineValidation.Validate() != nil ||
		request.BaselineValidation.Type != snapshot.TypeRef("validation/v1") ||
		request.Candidate.Validate() != nil ||
		request.Candidate.Type != snapshot.TypeRef("repository-change/v1") ||
		request.Validation.Validate() != nil ||
		request.Validation.Type != snapshot.TypeRef("validation/v1") ||
		request.Impact.Validate() != nil ||
		request.Impact.Type != snapshot.TypeRef("publish-impact/v1") ||
		request.Response.Validate() != nil ||
		request.Response.Type != snapshot.TypeRef("pull-request-response/v1") ||
		request.AcceptedReview.Kind != EvidenceAcceptedReview ||
		request.AcceptedReview.AcceptedReview == nil ||
		request.AcceptedReview.HumanWait != nil ||
		request.AcceptedReview.Validate() != nil ||
		request.Body.Validate(nil) != nil ||
		request.Body.BaselineDigest != request.Baseline.Digest.String() ||
		request.Body.CandidateDigest != request.Candidate.Digest.String() {
		return fmt.Errorf("%w: PR impact verification request is invalid", ErrInvalidRequest)
	}
	return nil
}

type PRImpactVerifier interface {
	VerifyPRImpact(
		context.Context,
		PRImpactVerificationRequest,
	) (contracts.PublishImpactBody, error)
}
