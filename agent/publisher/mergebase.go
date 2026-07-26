package publisher

import (
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// MergeBaseParameter names the merge base assertion carried in a publication
// parameter map.
//
// DECISION (2026-07-24): this value is SERVER-DERIVED and MUST NOT be
// authored. It names the commit the delivered change is based on, which is
// only knowable once the change exists — i.e. mid-run, after the step that
// rebased it. A statically authored workflow cannot express it, and the
// previous attempt to do so put a fake 40-hex placeholder in the
// merge-delivery seed, which made the seed unrunnable.
//
// Both boundaries that need the assertion — the await_snapshot merge approval
// intent and the publish_snapshot merge publication — bind the SAME
// repository-change/v1 snapshot. The value is therefore derived from that
// snapshot's sealed intrinsic metadata (see MergeBaseFromChange), so the two
// agree by construction rather than by an author keeping two YAML literals in
// sync.
//
// This is an assertion, not the safety property. Stale-base protection lives
// in GitService.Execute: it reads the destination's CURRENT base from the
// backend and refuses to publish when it differs from the change's base_sha
// (StatusStaleBase for merges, StatusRebaseRequired otherwise). Do not
// re-introduce an authored placeholder in the belief that it is the gate.
const MergeBaseParameter = "expected_base_sha"

// mergeBaseWireProbe is a syntactically valid stand-in used only by wire
// validation, where the authored shape is checked before any snapshot exists.
// It sits alongside the other probe values used for that purpose (team 1,
// build 1, all-zero digests) and never reaches a durable request.
const mergeBaseWireProbe = "0000000000000000000000000000000000000000"

// RejectAuthoredMergeBase fails when a workflow author wrote the server-owned
// merge base assertion into a parameter map.
func RejectAuthoredMergeBase(parameters map[string]string) error {
	if _, authored := parameters[MergeBaseParameter]; authored {
		return fmt.Errorf("%w: %s is server-derived from the bound repository-change/v1 input and must not be authored",
			ErrInvalidRequest, MergeBaseParameter)
	}
	return nil
}

// StampMergeBase returns a copy of authored merge parameters with the
// server-derived base assertion added. It refuses to overwrite an authored
// value so a plan can never smuggle one past the execution boundary.
func StampMergeBase(parameters map[string]string, baseSHA string) (map[string]string, error) {
	if err := RejectAuthoredMergeBase(parameters); err != nil {
		return nil, err
	}
	if !validGitObjectID(baseSHA) {
		return nil, fmt.Errorf("%w: derived merge base is not a complete Git object ID", ErrInvalidRequest)
	}
	stamped := cloneParameters(parameters)
	if stamped == nil {
		stamped = make(map[string]string, 1)
	}
	stamped[MergeBaseParameter] = baseSHA
	return stamped, nil
}

// ProbeMergeBase stamps the wire-validation placeholder. Callers validating an
// authored shape (before any snapshot is bound) use this so the shared
// publication validators can run unchanged; runtime callers must use
// StampMergeBase with a value from MergeBaseFromChange instead.
func ProbeMergeBase(parameters map[string]string) (map[string]string, error) {
	return StampMergeBase(parameters, mergeBaseWireProbe)
}

// MergeBaseFromChange reads the base commit of a sealed repository-change/v1
// snapshot from its intrinsic metadata. That metadata is written by the
// repository-change contract validator at seal time, which has already proven
// base_sha equals the HEAD of the declared base repository input, so it is
// server-owned evidence rather than a re-read of author-controlled bytes.
func MergeBaseFromChange(manifest snapshot.Snapshot) (string, error) {
	if manifest.Type != snapshot.TypeRef("repository-change/v1") {
		return "", fmt.Errorf("%w: merge base requires a repository-change/v1 snapshot", ErrInvalidRequest)
	}
	if len(manifest.IntrinsicMetadata) == 0 {
		return "", fmt.Errorf("%w: repository-change/v1 snapshot carries no sealed merge base", ErrInvalidRequest)
	}
	// Shared with the gateway inspector so both readers tolerate exactly the same
	// set of sealed shapes: the current one and the pre-rename one, nothing else.
	metadata, err := contracts.DecodeRepositoryChangeMetadata(manifest.IntrinsicMetadata)
	if err != nil {
		return "", fmt.Errorf("%w: repository-change/v1 intrinsic metadata is unreadable", ErrInvalidRequest)
	}
	if !validGitObjectID(metadata.BaseSHA) {
		return "", fmt.Errorf("%w: repository-change/v1 base_sha is not a complete Git object ID", ErrInvalidRequest)
	}
	return metadata.BaseSHA, nil
}

// AuthoredMergeIntent is exactly the part of a merge approval a workflow
// author writes. It deliberately has no base assertion field.
type AuthoredMergeIntent struct {
	Publisher             snapshot.TypeRef
	Destination           string
	Parameters            map[string]string
	ApprovalPolicyVersion string
}

// ValidateAuthoredMergeIntent checks an authored merge approval intent using
// the same rules the durable approval verifier applies, with the
// server-derived base assertion stood in for by a wire probe.
func ValidateAuthoredMergeIntent(intent AuthoredMergeIntent) error {
	parameters, err := ProbeMergeBase(intent.Parameters)
	if err != nil {
		return err
	}
	_, err = BuildMergeApprovalContext(MergeApprovalRequest{
		TeamID: 1, WorkflowRunID: 1, BuildID: 1,
		Input: snapshot.SnapshotRef{
			ID: 1, Type: snapshot.TypeRef("repository-change/v1"),
			Digest: snapshot.Digest("sha256:" + strings.Repeat("0", 64)),
		},
		Publisher: intent.Publisher, Mode: ModeMerge,
		Destination: intent.Destination, Parameters: parameters,
		ExpectedBaseSHA:       mergeBaseWireProbe,
		ApprovalPolicyVersion: intent.ApprovalPolicyVersion,
	})
	return err
}
