package atc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestAwaitSnapshotPRApprovalWireIsTypedAndExclusive(t *testing.T) {
	var step atc.Step
	err := json.Unmarshal([]byte(`{
		"await_snapshot":"reapproval",
		"pr_approval":{
			"binding_id":41,
			"action_digest":"sha256:`+strings.Repeat("1", 64)+`",
			"observation":"pull-request",
				"candidate":"candidate",
				"impact":"publish-impact",
				"response":"response",
				"destination":"github.example/acme/widget",
				"approval_policy_version":"engineering/v3",
				"prompt":"Approve this exact pull request revision?",
				"accepted_review":{
					"review":"accepted-review",
					"candidate":"accepted-candidate",
					"validation":"accepted-validation",
					"review_workflow_run_id":"7",
					"outcome_revision":2
				}
		},
		"validation":"validation",
		"type":"human-answer/v1",
		"on_timeout":"fail",
		"workflow_run_id":"((workflow_run_id))"
	}`), &step)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	wait, ok := step.Config.(*atc.AwaitSnapshotStep)
	if !ok {
		t.Fatalf("step config = %T, want *AwaitSnapshotStep", step.Config)
	}
	if wait.PRApproval == nil ||
		wait.PRApproval.BindingID != 41 ||
		wait.PRApproval.Observation != "pull-request" ||
		wait.PRApproval.Candidate != "candidate" ||
		wait.PRApproval.Impact != "publish-impact" ||
		wait.PRApproval.Response != "response" ||
		wait.PRApproval.Destination != "github.example/acme/widget" ||
		wait.PRApproval.AcceptedReview == nil ||
		wait.PRApproval.AcceptedReview.Candidate != "accepted-candidate" {
		t.Fatalf("PRApproval = %#v, want exact typed intent", wait.PRApproval)
	}

	for name, fragment := range map[string]string{
		"generic question": `"question":"question",`,
		"merge approval":   `"merge_approval":{"input":"candidate","publisher":"git-publisher/v1","destination":"git.example/repo","parameters":{"target_branch":"main"},"approval_policy_version":"engineering/v3","prompt":"Merge?"},`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := `{
				"await_snapshot":"reapproval",
				` + fragment + `
				"pr_approval":{
					"binding_id":41,
					"action_digest":"sha256:` + strings.Repeat("1", 64) + `",
					"observation":"pull-request",
						"candidate":"candidate",
						"impact":"publish-impact",
						"response":"response",
						"destination":"github.example/acme/widget",
						"approval_policy_version":"engineering/v3",
						"prompt":"Approve?",
						"accepted_review":{
							"review":"accepted-review",
							"candidate":"accepted-candidate",
							"validation":"accepted-validation",
							"review_workflow_run_id":"7",
							"outcome_revision":2
						}
				},
				"validation":"validation",
				"type":"human-answer/v1",
				"on_timeout":"fail"
			}`
			var invalid atc.Step
			if err := json.Unmarshal([]byte(payload), &invalid); err == nil {
				t.Fatalf("Unmarshal() error = nil, want %s exclusivity rejection", name)
			}
		})
	}
}

func TestPublishSnapshotPRApprovalWireRequiresExactLinkage(t *testing.T) {
	payload := `{
		"publish_snapshot":"publish",
		"publisher":"git-publisher/v1",
		"input":"candidate",
		"input_type":"repository-change/v1",
		"destination":"git.example/repo",
		"mode":"pull-request",
		"parameters":{"source_branch":"feature","target_branch":"main"},
		"approval_policy_version":"engineering/v3",
		"approval":"reapproval",
		"workflow_run_id":"((workflow_run_id))",
		"validation":"validation",
		"pr_approval":{
			"binding_id":41,
			"action_digest":"sha256:` + strings.Repeat("1", 64) + `",
			"observation":"pull-request",
			"impact":"publish-impact",
			"response":"response",
			"accepted_review":{
				"review":"accepted-review",
				"candidate":"accepted-candidate",
				"validation":"accepted-validation",
				"review_workflow_run_id":"7",
				"outcome_revision":2
			}
		}
	}`
	var step atc.Step
	if err := json.Unmarshal([]byte(payload), &step); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	publish, ok := step.Config.(*atc.PublishSnapshotStep)
	if !ok {
		t.Fatalf("step config = %T, want *PublishSnapshotStep", step.Config)
	}
	if publish.PRApproval == nil ||
		publish.PRApproval.BindingID != 41 ||
		publish.PRApproval.Observation != "pull-request" ||
		publish.PRApproval.Impact != "publish-impact" ||
		publish.PRApproval.Response != "response" ||
		publish.PRApproval.AcceptedReview == nil ||
		publish.PRApproval.AcceptedReview.ReviewWorkflowRunID != "7" {
		t.Fatalf("PRApproval = %#v, want exact publication linkage", publish.PRApproval)
	}

	for name, mutate := range map[string]func(map[string]any){
		"missing approval":     func(document map[string]any) { delete(document, "approval") },
		"missing workflow run": func(document map[string]any) { delete(document, "workflow_run_id") },
		"wrong mode":           func(document map[string]any) { document["mode"] = "branch" },
	} {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(payload), &document); err != nil {
				t.Fatal(err)
			}
			mutate(document)
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			var invalid atc.Step
			if err := json.Unmarshal(raw, &invalid); err == nil {
				t.Fatalf("Unmarshal() error = nil, want %s rejection", name)
			}
		})
	}
}
