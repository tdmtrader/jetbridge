package pullrequest

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestCreateBindingRequiresPersistedPublicationTargetAuthority(t *testing.T) {
	request := CreateBinding{
		TeamID: 7,
		Locator: Locator{
			Provider: ProviderGitHub, Repository: "acme/widget",
			ExternalID: "118",
		},
		URL:                              "https://github.example/acme/widget/pull/118",
		SourceRef:                        "refs/heads/agent/change",
		TargetRef:                        "refs/heads/main",
		Destination:                      "github.example/acme/widget",
		ApprovalPolicyVersion:            "engineering/v3",
		OriginatingWorkflowRunID:         snapshot.WorkflowRunID(19),
		OriginatingPublicationOccurrence: 23,
		CreationPublicationOccurrenceID:  24,
		MonitorWorkflowDefinitionID:      29,
		MonitorWorkflowVersion:           3,
		AcknowledgedCursor:               Cursor(`{"cursor":"initial"}`),
		LastObservationSnapshotID:        snapshot.SnapshotID(31),
		LastReconciledSourceSHA:          strings.Repeat("a", 40),
		LastReconciledTargetSHA:          strings.Repeat("b", 40),
		LastReconciledAt:                 time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid binding authority rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*CreateBinding)
	}{
		{
			name: "missing destination",
			mutate: func(candidate *CreateBinding) {
				candidate.Destination = ""
			},
		},
		{
			name: "missing approval policy",
			mutate: func(candidate *CreateBinding) {
				candidate.ApprovalPolicyVersion = ""
			},
		},
		{
			name: "missing creation publication",
			mutate: func(candidate *CreateBinding) {
				candidate.CreationPublicationOccurrenceID = 0
			},
		},
		{
			name: "unbounded approval policy",
			mutate: func(candidate *CreateBinding) {
				candidate.ApprovalPolicyVersion = strings.Repeat("x", 129)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid publication target authority was accepted")
			}
		})
	}
}
