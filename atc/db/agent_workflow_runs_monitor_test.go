package db

import (
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestValidateAgentWorkflowRunPRMonitorAttachmentRequiresFourExactAuthorityInputs(
	t *testing.T,
) {
	attachment := AgentWorkflowRunPRMonitorAttachment{
		BindingID: 9, SourcePipelineID: 13,
		SelectingBuildID: 301, SourceAdmissionID: 41,
		ExpectedBindingRevision: 8, BaseBindingRevision: 7,
		ActionDigest:                    "sha256:" + strings.Repeat("d", 64),
		ReservationToken:                "reservation-token",
		ObservationSnapshotID:           501,
		Cursor:                          "cursor-1",
		SourceSHA:                       strings.Repeat("3", 40),
		TargetSHA:                       strings.Repeat("4", 40),
		AcceptedPublicationOccurrenceID: 81,
		AcceptedReview: snapshot.SnapshotRef{
			ID: 502, Type: "review/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("5", 64)),
		},
		AcceptedCandidate: snapshot.SnapshotRef{
			ID: 503, Type: "repository/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("6", 64)),
		},
		AcceptedValidation: snapshot.SnapshotRef{
			ID: 504, Type: "validation/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("7", 64)),
		},
		AcceptedReviewWorkflowRunID: 71,
		AcceptedOutcomeRevision:     3,
		SelectedVersion: atc.Version{
			"provider": "github", "external_id": "42",
			"source_sha":    strings.Repeat("3", 40),
			"target_sha":    strings.Repeat("4", 40),
			"action_kind":   "review_batch",
			"action_digest": "sha256:" + strings.Repeat("d", 64),
			"cursor":        "cursor-1", "binding_revision": "7",
		},
	}
	admissionID := attachment.SourceAdmissionID
	request := AgentWorkflowRunCreateRequest{
		DefinitionKind:            workflow.DefinitionKindWorkflow,
		OriginKind:                "pr-monitor",
		OriginReference:           strconv.FormatInt(attachment.BindingID, 10),
		ResourceSourceAdmissionID: &admissionID,
		Inputs: map[string]snapshot.SnapshotRef{
			pullrequest.MonitorSourceName: {
				ID:   attachment.ObservationSnapshotID,
				Type: "pull-request/v1",
				Digest: snapshot.Digest(
					"sha256:" + strings.Repeat("8", 64),
				),
			},
			pullrequest.MonitorAcceptedReviewInputName:     attachment.AcceptedReview,
			pullrequest.MonitorAcceptedCandidateInputName:  attachment.AcceptedCandidate,
			pullrequest.MonitorAcceptedValidationInputName: attachment.AcceptedValidation,
		},
	}
	if err := validateAgentWorkflowRunPRMonitorAttachment(
		request, attachment,
	); err != nil {
		t.Fatalf("exact monitor attachment: %v", err)
	}

	substituted := request
	substituted.Inputs = cloneSnapshotRefs(request.Inputs)
	value := substituted.Inputs[pullrequest.MonitorAcceptedCandidateInputName]
	value.ID++
	substituted.Inputs[pullrequest.MonitorAcceptedCandidateInputName] = value
	if err := validateAgentWorkflowRunPRMonitorAttachment(
		substituted, attachment,
	); err == nil {
		t.Fatal("accepted candidate substitution was authorized")
	}

	extra := request
	extra.Inputs = cloneSnapshotRefs(request.Inputs)
	extra.Inputs["caller-extra"] = attachment.AcceptedReview
	if err := validateAgentWorkflowRunPRMonitorAttachment(
		extra, attachment,
	); err == nil {
		t.Fatal("extra monitor input was authorized")
	}
}

func cloneSnapshotRefs(
	values map[string]snapshot.SnapshotRef,
) map[string]snapshot.SnapshotRef {
	cloned := make(map[string]snapshot.SnapshotRef, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
