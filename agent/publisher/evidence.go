package publisher

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type EvidenceVerifier interface {
	Verify(context.Context, EvidenceRequest) (PublicationEvidence, error)
}

type StoreEvidenceVerifier struct {
	runs          ReviewRunEvidenceResolver
	outcomes      ReviewOutcomeReader
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	approvals     ExactApprovalVerifier
	canonicalizer snapshot.Canonicalizer
	registry      snapshot.ValidatorRegistry
}

func NewEvidenceVerifier(
	runs ReviewRunEvidenceResolver,
	outcomes ReviewOutcomeReader,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	approvals ExactApprovalVerifier,
	canonicalizer snapshot.Canonicalizer,
) (*StoreEvidenceVerifier, error) {
	if nilInterface(runs) || nilInterface(outcomes) || nilInterface(metadata) ||
		nilInterface(content) || nilInterface(approvals) {
		return nil, fmt.Errorf("publisher: evidence run, outcome, snapshot, and approval stores are required")
	}
	if err := snapshot.ValidateTempDir(canonicalizer.TempDir); err != nil {
		return nil, fmt.Errorf("publisher: evidence canonicalizer: %w", err)
	}
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return nil, fmt.Errorf("publisher: evidence validator registry: %w", err)
	}
	return &StoreEvidenceVerifier{
		runs: runs, outcomes: outcomes, metadata: metadata, content: content,
		approvals: approvals, canonicalizer: canonicalizer, registry: registry,
	}, nil
}

func (verifier *StoreEvidenceVerifier) Verify(
	ctx context.Context,
	request EvidenceRequest,
) (PublicationEvidence, error) {
	if verifier == nil || ctx == nil {
		return PublicationEvidence{}, fmt.Errorf("%w: evidence verifier and context are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return PublicationEvidence{}, err
	}
	request = request.clone()
	if err := request.validate(); err != nil {
		return PublicationEvidence{}, err
	}
	if request.HumanWait != nil {
		approval, err := verifier.approvals.VerifyExact(ctx, *request.HumanWait)
		if err != nil {
			return PublicationEvidence{}, err
		}
		evidence := PublicationEvidence{Kind: EvidenceHumanWait, HumanWait: &approval}
		if err := evidence.Validate(); err != nil {
			return PublicationEvidence{}, err
		}
		return evidence, nil
	}
	return verifier.verifyAcceptedReview(ctx, request.TeamID, *request.AcceptedReview)
}

var _ EvidenceVerifier = (*StoreEvidenceVerifier)(nil)
