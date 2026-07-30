package workflow_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/atc"
)

func TestPRMonitorSeedBindsOneExactRevisionWithoutExposingForgeAuthority(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("seeds/pr-monitor-v3")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflowtest.NewMemoryStore().ImportManifest(
		"pr-monitor", manifest, "seed-test",
	)
	if err != nil {
		t.Fatalf("compile/import seed: %v", err)
	}
	if definition.Name != "pr-monitor" ||
		definition.SchemaVersion != 3 ||
		definition.SignatureVersion != 1 {
		t.Fatalf(
			"seed identity = name %q schema %d signature %d",
			definition.Name, definition.SchemaVersion, definition.SignatureVersion,
		)
	}

	plan := definition.Compiled.Function.Plan
	if len(plan) != 8 {
		t.Fatalf("monitor plan has %d steps, want 8", len(plan))
	}

	materialize := requirePRMonitorTask(t, plan[0], "materialize-pr")
	if got, want := materialize.Config.Run.Path, "function-runner"; got != want {
		t.Fatalf("materializer path = %q, want %q", got, want)
	}
	if got, want := materialize.Config.Run.Args, []string{
		"pr-monitor-materialize",
		"--observation=pull-request",
		"--source-output=source-repository",
		"--target-output=target-repository",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("materializer args = %q, want %q", got, want)
	}
	requirePRMonitorSnapshotTypes(t, materialize.SnapshotInputs, map[string]snapshot.TypeRef{
		"pull-request": "pull-request/v1",
	})
	requirePRMonitorOutputTypes(t, materialize.SnapshotOutputs, map[string]snapshot.TypeRef{
		"source-repository": "repository/v1",
		"target-repository": "repository/v1",
	})

	respond, ok := plan[1].Config.(*atc.AgentStep)
	if !ok || respond.Name != "respond" {
		t.Fatalf("response step = %T %#v, want agent respond", plan[1].Config, plan[1].Config)
	}
	wantResponseInputs := []string{
		"source-repository",
		"pull-request",
		"accepted-review",
		"accepted-candidate",
		"accepted-validation",
	}
	if !reflect.DeepEqual(respond.Inputs, wantResponseInputs) {
		t.Fatalf("response agent inputs = %q, want %q", respond.Inputs, wantResponseInputs)
	}
	if containsString(respond.Inputs, "target-repository") {
		t.Fatal("response agent received the target repository instead of only current PR work")
	}
	requirePRMonitorSnapshotTypes(t, respond.SnapshotInputs, map[string]snapshot.TypeRef{
		"source-repository":   "repository/v1",
		"pull-request":        "pull-request/v1",
		"accepted-review":     "review/v1",
		"accepted-candidate":  "repository/v1",
		"accepted-validation": "validation/v1",
	})
	requirePRMonitorOutputTypes(t, respond.SnapshotOutputs, map[string]snapshot.TypeRef{
		"draft-change":   "repository-change/v1",
		"response-draft": "pull-request-response/v1",
	})
	if len(respond.Sidecars) != 0 || len(respond.Capabilities) != 0 {
		t.Fatalf(
			"response agent received sidecar/capability authority: sidecars=%#v capabilities=%q",
			respond.Sidecars, respond.Capabilities,
		)
	}
	for name := range respond.Env {
		lower := strings.ToLower(name)
		for _, forbidden := range []string{"credential", "password", "secret", "token"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("response agent environment exposes forge authority through %q", name)
			}
		}
	}
	for _, required := range []string{
		"Preserve every commit and change already present in `source-repository`",
		"only thread IDs listed in the completed review batch",
		"Do not contact the forge",
		"attempt to complete the pull request",
		"`source-repository` as the one `base` subject",
	} {
		if !strings.Contains(respond.Prompt, required) {
			t.Fatalf("response prompt does not contain %q:\n%s", required, respond.Prompt)
		}
	}

	authorize := requirePRMonitorTask(t, plan[2], "authorize-response")
	if got, want := authorize.Config.Run.Args, []string{
		"authorize-pr-response",
		"--observation=pull-request",
		"--draft=response-draft",
		"--output=response",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("response authorization args = %q, want %q", got, want)
	}
	requirePRMonitorSnapshotTypes(t, authorize.SnapshotInputs, map[string]snapshot.TypeRef{
		"pull-request":   "pull-request/v1",
		"response-draft": "pull-request-response/v1",
	})
	requirePRMonitorOutputTypes(t, authorize.SnapshotOutputs, map[string]snapshot.TypeRef{
		"response": "pull-request-response/v1",
	})

	rebase := requirePRMonitorTask(t, plan[3], "rebase-revision")
	if got, want := rebase.Config.Run.Args, []string{
		"merge-prepare",
		"--candidate=draft-change",
		"--target=target-repository",
		"--base=source-repository",
		"--output=candidate",
		"--method=rebase",
		"--message=Refresh pull request revision",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rebase args = %q, want %q", got, want)
	}
	requirePRMonitorSnapshotTypes(t, rebase.SnapshotInputs, map[string]snapshot.TypeRef{
		"draft-change":      "repository-change/v1",
		"source-repository": "repository/v1",
		"target-repository": "repository/v1",
	})
	requirePRMonitorOutputTypes(t, rebase.SnapshotOutputs, map[string]snapshot.TypeRef{
		"candidate": "repository-change/v1",
	})

	validate := requirePRMonitorTask(t, plan[4], "validate-revision")
	authority := validate.DevValidationAuthority
	if authority == nil ||
		authority.CandidateInput != "candidate" ||
		!reflect.DeepEqual(authority.BaseInputs, []atc.DevValidationBaseInput{{
			Name: "target-repository", Type: "repository/v1",
		}}) {
		t.Fatalf("validation authority = %#v, want candidate against exact target", authority)
	}
	if validate.SnapshotOutputs["validation"].Type != snapshot.TypeRef("validation/v1") {
		t.Fatalf("validation output = %#v", validate.SnapshotOutputs)
	}

	impact, ok := plan[5].Config.(*atc.AgentStep)
	if !ok || impact.Name != "assess-impact" {
		t.Fatalf("impact step = %T %#v, want agent assess-impact", plan[5].Config, plan[5].Config)
	}
	if !reflect.DeepEqual(impact.Inputs, []string{
		"accepted-candidate",
		"accepted-validation",
		"candidate",
		"validation",
		"pull-request",
		"response",
	}) {
		t.Fatalf("impact inputs = %q", impact.Inputs)
	}
	requirePRMonitorOutputTypes(t, impact.SnapshotOutputs, map[string]snapshot.TypeRef{
		"publish-impact": "publish-impact/v1",
	})
	if len(impact.Sidecars) != 0 || len(impact.Capabilities) != 0 {
		t.Fatalf("impact agent received sidecar/capability authority")
	}

	timeout, ok := plan[6].Config.(*atc.TimeoutStep)
	if !ok || timeout.Duration != "72h" {
		t.Fatalf("approval wrapper = %T %#v, want 72h timeout", plan[6].Config, plan[6].Config)
	}
	wait, ok := timeout.Step.(*atc.AwaitSnapshotStep)
	if !ok || wait.Name != "reapproval" || wait.PRApproval == nil {
		t.Fatalf("approval wait = %T %#v, want typed PR approval", timeout.Step, timeout.Step)
	}
	requirePRMonitorApprovalIntent(t, wait.PRApproval)
	if wait.Validation != "validation" ||
		wait.Type != snapshot.TypeRef("human-answer/v1") ||
		wait.OnTimeout != atc.AwaitSnapshotOnTimeoutFail {
		t.Fatalf("approval wait contract = %#v", wait)
	}

	publication, ok := plan[7].Config.(*atc.PublishSnapshotStep)
	if !ok || publication.Name != "publish-revision" {
		t.Fatalf("publication step = %T %#v", plan[7].Config, plan[7].Config)
	}
	if publication.Publisher != publisher.GitPublisher ||
		publication.Mode != publisher.ModePullRequest ||
		publication.Input != "candidate" ||
		publication.InputType != snapshot.TypeRef("repository-change/v1") ||
		publication.Validation != "validation" ||
		publication.Approval != "reapproval" ||
		publication.PRApproval == nil {
		t.Fatalf("PR-only typed publication = %#v", publication)
	}
	if !reflect.DeepEqual(publication.Parameters, map[string]string{
		"source_branch": "refs/heads/agent/pr-revision",
		"target_branch": "refs/heads/main",
	}) {
		t.Fatalf("publication branch authority = %#v", publication.Parameters)
	}
	requirePRMonitorPublicationIntent(t, publication.PRApproval)
}

func requirePRMonitorApprovalIntent(t *testing.T, intent *atc.PRApprovalIntent) {
	t.Helper()
	if intent.BindingID != workflow.PRMonitorBindingIDSentinel ||
		intent.ActionDigest != workflow.PRMonitorActionDigestSentinel ||
		intent.Observation != "pull-request" ||
		intent.Candidate != "candidate" ||
		intent.Impact != "publish-impact" ||
		intent.Response != "response" ||
		intent.AcceptedReview == nil {
		t.Fatalf("PR approval intent = %#v", intent)
	}
	requirePRMonitorAcceptedReviewIntent(t, intent.AcceptedReview)
}

func requirePRMonitorPublicationIntent(
	t *testing.T,
	intent *atc.PRApprovalPublicationIntent,
) {
	t.Helper()
	if intent.BindingID != workflow.PRMonitorBindingIDSentinel ||
		intent.ActionDigest != workflow.PRMonitorActionDigestSentinel ||
		intent.Observation != "pull-request" ||
		intent.Impact != "publish-impact" ||
		intent.Response != "response" ||
		intent.AcceptedReview == nil {
		t.Fatalf("PR publication intent = %#v", intent)
	}
	requirePRMonitorAcceptedReviewIntent(t, intent.AcceptedReview)
}

func requirePRMonitorAcceptedReviewIntent(
	t *testing.T,
	intent *atc.PRAcceptedReviewIntent,
) {
	t.Helper()
	if intent.Review != "accepted-review" ||
		intent.Candidate != "accepted-candidate" ||
		intent.Validation != "accepted-validation" ||
		intent.ReviewWorkflowRunID !=
			workflow.PRMonitorReviewWorkflowRunIDSentinel ||
		intent.OutcomeRevision !=
			workflow.PRMonitorAcceptedOutcomeRevisionSentinel {
		t.Fatalf("accepted review sentinel intent = %#v", intent)
	}
}

func requirePRMonitorTask(t *testing.T, step atc.Step, name string) *atc.TaskStep {
	t.Helper()
	task, ok := step.Config.(*atc.TaskStep)
	if !ok || task.Name != name || task.Config == nil {
		t.Fatalf("step %q = %T %#v, want configured task", name, step.Config, step.Config)
	}
	return task
}

func requirePRMonitorSnapshotTypes(
	t *testing.T,
	got map[string]atc.SnapshotInputConfig,
	want map[string]snapshot.TypeRef,
) {
	t.Helper()
	types := make(map[string]snapshot.TypeRef, len(got))
	for name, declaration := range got {
		types[name] = declaration.Type
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("snapshot input types = %#v, want %#v", types, want)
	}
}

func requirePRMonitorOutputTypes(
	t *testing.T,
	got map[string]atc.SnapshotOutputConfig,
	want map[string]snapshot.TypeRef,
) {
	t.Helper()
	types := make(map[string]snapshot.TypeRef, len(got))
	for name, declaration := range got {
		types[name] = declaration.Type
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("snapshot output types = %#v, want %#v", types, want)
	}
}

func containsString(values []string, expected string) bool {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	index := sort.SearchStrings(sorted, expected)
	return index < len(sorted) && sorted[index] == expected
}
