package publisher_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

func TestOperationKeyIsCanonicalAndDistinguishesSemanticOperations(t *testing.T) {
	base := publisher.Request{
		Publisher: publisher.GitPublisher, Input: changeRef(),
		Destination: "github.example/team/repo", Mode: publisher.ModeBranch,
		Parameters:            map[string]string{"target_branch": "main", "source_branch": "agent/upgrade"},
		ApprovalPolicyVersion: "engineering/v2",
		Authority:             publicationAuthority(),
	}
	first, err := base.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Parameters = map[string]string{"source_branch": "agent/upgrade", "target_branch": "main"}
	second, err := reordered.OperationKey()
	if err != nil || second != first {
		t.Fatalf("canonical keys = %q/%q, %v", first, second, err)
	}
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("operation key = %q", first)
	}

	mutations := []func(*publisher.Request){
		func(request *publisher.Request) { request.Authority.TeamID++ },
		func(request *publisher.Request) { request.Input.ID++ },
		func(request *publisher.Request) { request.Destination += "-fork" },
		// Branch and merge are the only Git modes left, so the one expressible
		// mode change necessarily carries merge's mandatory base assertion and
		// approval evidence with it.
		func(request *publisher.Request) {
			request.Mode = publisher.ModeMerge
			delete(request.Parameters, "source_branch")
			request.Parameters[publisher.MergeBaseParameter] = strings.Repeat("a", 40)
			request.ApprovedBy = "alice"
			request.Approval = approvalEvidence("alice", 11)
		},
		func(request *publisher.Request) { request.Parameters["target_branch"] = "stable" },
		func(request *publisher.Request) { request.ApprovalPolicyVersion = "engineering/v3" },
	}
	for index, mutate := range mutations {
		candidate := base.Clone()
		mutate(&candidate)
		key, err := candidate.OperationKey()
		if err != nil {
			t.Fatalf("mutation %d: %v", index, err)
		}
		if key == first {
			t.Errorf("mutation %d did not change operation key", index)
		}
	}
	sameOperationFromAnotherRun := base.Clone()
	sameOperationFromAnotherRun.Authority.BuildID++
	sameOperationFromAnotherRun.Authority.WorkflowRunID++
	sameOperationFromAnotherRun.Authority.Actor = "bob"
	retryKey, err := sameOperationFromAnotherRun.OperationKey()
	if err != nil || retryKey != first {
		t.Fatalf("semantic retry key = %q, want %q: %v", retryKey, first, err)
	}
}

func TestApprovalEvidenceUsesLosslessWaitIdentityJSON(t *testing.T) {
	evidence := approvalEvidence("alice", 9007199254740993)
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"wait_id":"9007199254740993"`) {
		t.Fatalf("approval evidence JSON = %s", encoded)
	}
	var decoded publisher.ApprovalEvidence
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WaitID != evidence.WaitID {
		t.Fatalf("wait ID = %s, want %s", decoded.WaitID.String(), evidence.WaitID.String())
	}
}

func TestRequestValidatesPublisherModeInputAndApproval(t *testing.T) {
	valid := publisher.Request{
		Publisher: publisher.GitPublisher, Input: changeRef(), Destination: "github.example/team/repo",
		Mode: publisher.ModeMerge, Parameters: map[string]string{"target_branch": "main", "expected_base_sha": strings.Repeat("a", 40)},
		ApprovalPolicyVersion: "merge/v1", ApprovedBy: "alice", Approval: approvalEvidence("alice", 11),
		Authority: publicationAuthority(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := []func(*publisher.Request){
		func(request *publisher.Request) { request.Authority.TeamID = 0 },
		func(request *publisher.Request) { request.Authority.TeamName = "" },
		func(request *publisher.Request) { request.Authority.BuildID = 0 },
		func(request *publisher.Request) { request.Authority.Actor = "" },
		func(request *publisher.Request) { request.Publisher = "unknown/v1" },
		func(request *publisher.Request) { request.Input.Type = "review/v1" },
		func(request *publisher.Request) { request.Destination = " " },
		func(request *publisher.Request) { request.Mode = publisher.ModeComment },
		func(request *publisher.Request) { request.ApprovalPolicyVersion = "" },
		func(request *publisher.Request) { request.ApprovedBy = "" },
		func(request *publisher.Request) { request.Approval = nil },
		func(request *publisher.Request) { request.Approval.ResolvedBy = "mallory" },
		func(request *publisher.Request) { request.Parameters = map[string]string{"": "value"} },
		func(request *publisher.Request) { request.Parameters = map[string]string{"approved_by": "mallory"} },
	}
	for index, mutate := range invalid {
		candidate := valid.Clone()
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid case %d accepted: %+v", index, candidate)
		}
	}

	workItem := publisher.Request{
		Publisher: publisher.WorkItemPublisher, Input: snapshot.SnapshotRef{
			ID: 8, Type: "review/v1", Digest: digest("b"),
		}, Destination: "JIRA-42", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "review complete"}, ApprovalPolicyVersion: "comment/v1",
		Authority: publicationAuthority(),
	}
	if err := workItem.Validate(); err != nil {
		t.Fatalf("work-item publisher: %v", err)
	}
}

func publicationAuthority() publisher.Authority {
	return publisher.Authority{
		TeamID: 17, TeamName: "main", BuildID: 42,
		WorkflowRunID: snapshot.WorkflowRunID(91), Actor: "alice",
	}
}

func changeRef() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: 7, Type: "repository-change/v1", Digest: digest("a")}
}

func digest(character string) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(character, 64))
}

func approvalEvidence(actor string, waitID int64) *publisher.ApprovalEvidence {
	return &publisher.ApprovalEvidence{
		WaitID:     workflowwait.ID(waitID),
		Question:   snapshot.SnapshotRef{ID: 101, Type: "question/v1", Digest: digest("c")},
		Answer:     snapshot.SnapshotRef{ID: 102, Type: "human-answer/v1", Digest: digest("d")},
		ResolvedBy: actor, ResolvedAt: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}
}
