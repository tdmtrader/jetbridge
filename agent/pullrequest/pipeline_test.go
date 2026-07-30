package pullrequest_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// Removing serial/version-every selection, exposing a literal credential, or
// letting mutable binding state reuse the old config hash must fail here.
func TestRenderMonitorPipelineUsesProtectedOrdinarySelection(t *testing.T) {
	binding := monitorPipelineBinding()
	target, err := pullrequest.MonitorPipelineTargetForBinding(
		binding,
		monitorPipelinePolicy(),
	)
	if err != nil {
		t.Fatalf("MonitorPipelineTargetForBinding: %v", err)
	}
	rendered, err := pullrequest.RenderMonitorPipeline(target)
	if err != nil {
		t.Fatalf("RenderMonitorPipeline: %v", err)
	}

	if rendered.Config.Template {
		t.Fatal("monitor pipeline is a template")
	}
	if len(rendered.Config.Resources) != 1 ||
		len(rendered.Config.ResourceTypes) != 1 ||
		len(rendered.Config.Jobs) != 1 {
		t.Fatalf("monitor config = %#v", rendered.Config)
	}
	resource := rendered.Config.Resources[0]
	if resource.Name != pullrequest.MonitorResourceName ||
		resource.Type != pullrequest.MonitorResourceTypeName ||
		resource.CheckEvery == nil ||
		resource.CheckEvery.Interval != 5*time.Minute {
		t.Fatalf("monitor resource = %#v", resource)
	}
	if got := resource.Source["read_token"]; got != "((engineering-github-read))" {
		t.Fatalf("read credential projection = %#v", got)
	}
	for key, want := range map[string]any{
		"provider":           "github",
		"repository":         "acme/widget",
		"external_id":        "118",
		"api_base_url":       "https://api.github.example",
		"repository_url":     "https://github.example/acme/widget.git",
		"poll_interval":      "5m0s",
		"freshness_interval": "6h0m0s",
	} {
		if got := resource.Source[key]; got != want {
			t.Fatalf("resource source %q = %#v, want %#v", key, got, want)
		}
	}
	monitor, ok := resource.Source["monitor"].(atc.Source)
	if !ok {
		t.Fatalf("monitor projection type = %T", resource.Source["monitor"])
	}
	if monitor["binding_id"] != binding.ID ||
		monitor["binding_revision"] != binding.Revision ||
		monitor["acknowledged_cursor"] != string(binding.AcknowledgedCursor) ||
		monitor["last_reconciled_target"] != binding.LastReconciledTargetSHA ||
		monitor["active_action_digest"] != binding.Active.ActionDigest {
		t.Fatalf("monitor projection = %#v", monitor)
	}
	resourceType := rendered.Config.ResourceTypes[0]
	if resourceType.Name != pullrequest.MonitorResourceTypeName ||
		resourceType.Image != monitorPipelinePolicy().ResourceType.Image {
		t.Fatalf("monitor resource type = %#v", resourceType)
	}
	job := rendered.Config.Jobs[0]
	if job.Name != pullrequest.MonitorJobName ||
		!job.Serial ||
		!job.DisableManualTrigger ||
		len(job.PlanSequence) != 1 {
		t.Fatalf("monitor job = %#v", job)
	}
	get, ok := job.PlanSequence[0].Config.(*atc.GetStep)
	if !ok ||
		get.Name != pullrequest.MonitorSourceName ||
		get.Resource != pullrequest.MonitorResourceName ||
		!get.Trigger ||
		get.Version == nil ||
		!get.Version.Every {
		t.Fatalf("monitor get = %#v", job.PlanSequence[0].Config)
	}
	if len(rendered.ConfigHash) != 64 ||
		!strings.HasPrefix(rendered.PipelineName, "agent-pr-monitor-118-") {
		t.Fatalf("rendered identity = %#v", rendered)
	}
	if bytes.Contains(rendered.CanonicalJSON, []byte("literal-secret")) {
		t.Fatal("literal credential reached canonical monitor config")
	}
}

// Reusing the old config hash after an acknowledgement would let Lidar emit a
// version against stale binding authority. Changing the physical identity
// would orphan the one binding-owned pipeline instead of reconfiguring it.
func TestRenderMonitorPipelineChangesConfigHashButKeepsBindingIdentity(t *testing.T) {
	binding := monitorPipelineBinding()
	firstTarget, err := pullrequest.MonitorPipelineTargetForBinding(
		binding,
		monitorPipelinePolicy(),
	)
	if err != nil {
		t.Fatalf("first target: %v", err)
	}
	first, err := pullrequest.RenderMonitorPipeline(firstTarget)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}

	next := binding
	next.Revision++
	next.AcknowledgedCursor = pullrequest.Cursor("cursor-2")
	next.LastReconciledTargetSHA = strings.Repeat("e", 40)
	next.LastReconciledAt = next.LastReconciledAt.Add(6 * time.Hour)
	next.Active = nil
	next.Paused = true
	nextTarget, err := pullrequest.MonitorPipelineTargetForBinding(
		next,
		monitorPipelinePolicy(),
	)
	if err != nil {
		t.Fatalf("next target: %v", err)
	}
	second, err := pullrequest.RenderMonitorPipeline(nextTarget)
	if err != nil {
		t.Fatalf("next render: %v", err)
	}

	if first.ConfigHash == second.ConfigHash {
		t.Fatalf("mutable binding state reused config hash %q", first.ConfigHash)
	}
	if first.PipelineName != second.PipelineName {
		t.Fatalf("binding pipeline identity changed: %q -> %q", first.PipelineName, second.PipelineName)
	}
}

// A public caller may mutate the diagnostic view, but the DB seam must reopen
// only the private render authority for the exact binding projection.
func TestRenderedMonitorPipelineRetainsPrivateBindingAuthority(t *testing.T) {
	binding := monitorPipelineBinding()
	target, err := pullrequest.MonitorPipelineTargetForBinding(
		binding,
		monitorPipelinePolicy(),
	)
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	rendered, err := pullrequest.RenderMonitorPipeline(target)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wantName, wantHash := rendered.PipelineName, rendered.ConfigHash
	wantCanonical := append([]byte(nil), rendered.CanonicalJSON...)

	rendered.PipelineName = "caller-substitution"
	rendered.ConfigHash = strings.Repeat("0", 64)
	rendered.CanonicalJSON = []byte(`{"caller":"substitution"}`)
	rendered.Config.Resources[0].Source["read_token"] = "literal-secret"

	protected, err := rendered.ProtectedForBinding(binding)
	if err != nil {
		t.Fatalf("ProtectedForBinding: %v", err)
	}
	if protected.PipelineName != wantName ||
		protected.ConfigHash != wantHash ||
		!bytes.Equal(protected.CanonicalJSON, wantCanonical) ||
		protected.Config.Resources[0].Source["read_token"] != "((engineering-github-read))" {
		t.Fatalf("protected render accepted public mutation: %#v", protected)
	}

	stale := binding
	stale.Revision--
	if _, err := rendered.ProtectedForBinding(stale); err == nil {
		t.Fatal("protected render accepted a stale binding revision")
	}
	wrongTeam := binding
	wrongTeam.TeamID++
	if _, err := rendered.ProtectedForBinding(wrongTeam); err == nil {
		t.Fatal("protected render accepted another team")
	}
}

func TestRenderMonitorPipelineRejectsUnsafePolicyAndProjection(t *testing.T) {
	binding := monitorPipelineBinding()
	tests := map[string]func(*pullrequest.MonitorPipelinePolicy, *pullrequest.Binding){
		"mutable resource image": func(policy *pullrequest.MonitorPipelinePolicy, _ *pullrequest.Binding) {
			policy.ResourceType.Image = "registry.example/forge-pr:latest"
		},
		"literal credential syntax": func(policy *pullrequest.MonitorPipelinePolicy, _ *pullrequest.Binding) {
			policy.ReadCredential = "((literal-secret))"
		},
		"credential-bearing repository URL": func(policy *pullrequest.MonitorPipelinePolicy, _ *pullrequest.Binding) {
			policy.RepositoryURL = "https://token@github.example/acme/widget.git"
		},
		"non-divisible freshness interval": func(policy *pullrequest.MonitorPipelinePolicy, _ *pullrequest.Binding) {
			policy.FreshnessInterval = 6*time.Hour + time.Second
		},
		"stale check revision": func(_ *pullrequest.MonitorPipelinePolicy, binding *pullrequest.Binding) {
			binding.Revision = 0
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := monitorPipelinePolicy()
			candidate := binding
			mutate(&policy, &candidate)
			target, err := pullrequest.MonitorPipelineTargetForBinding(candidate, policy)
			if err == nil {
				_, err = pullrequest.RenderMonitorPipeline(target)
			}
			if err == nil {
				t.Fatal("unsafe monitor target was accepted")
			}
		})
	}
}

// This characterization protects the pre-existing definition-owned renderer
// while Task 9 adds a separate binding-owned render path.
func TestDefinitionOwnedResourceSourceRenderingIsUnchanged(t *testing.T) {
	target := workflow.ResourceSourcePipelineTarget{
		TeamID: 7, WorkflowDefinitionID: 11,
		WorkflowName: "definition-source", WorkflowVersion: 2,
		Sources: []workflow.ResourceSource{{
			Name: "repository-source", Resource: "repository",
			Type: "repository/v1", Trigger: true,
		}},
		Resources: atc.ResourceConfigs{{
			Name: "repository", Type: "git",
			Source: atc.Source{"uri": "https://example.invalid/repository.git"},
		}},
	}
	first, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		t.Fatalf("definition render: %v", err)
	}
	second, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		t.Fatalf("repeated definition render: %v", err)
	}
	if first.PipelineName != second.PipelineName ||
		first.ConfigHash != second.ConfigHash ||
		!reflect.DeepEqual(first.Config, second.Config) ||
		!bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatal("definition-owned rendering is not deterministic")
	}
}

func TestMonitorPipelineReconcilerConvergesEachActiveBinding(t *testing.T) {
	first := monitorPipelineBinding()
	second := first
	second.ID++
	second.Locator.ExternalID = "119"
	second.Revision++
	second.Active = nil

	var resolved []int64
	var converged []pullrequest.RenderedMonitorPipeline
	reconciler, err := pullrequest.NewMonitorPipelineReconciler(
		first.TeamID,
		monitorBindingListerFunc(func(
			ctx context.Context,
			teamID int,
		) ([]pullrequest.Binding, error) {
			if ctx == nil || teamID != first.TeamID {
				t.Fatalf("binding list authority = (%v, %d)", ctx, teamID)
			}
			return []pullrequest.Binding{first, second}, nil
		}),
		monitorPolicyResolverFunc(func(
			ctx context.Context,
			binding pullrequest.Binding,
		) (pullrequest.MonitorPipelinePolicy, error) {
			if ctx == nil {
				t.Fatal("policy resolver received nil context")
			}
			resolved = append(resolved, binding.ID)
			return monitorPipelinePolicy(), nil
		}),
		monitorPipelineConvergerFunc(func(
			ctx context.Context,
			binding pullrequest.Binding,
			rendered pullrequest.RenderedMonitorPipeline,
		) (bool, error) {
			if ctx == nil {
				t.Fatal("converger received nil context")
			}
			protected, err := rendered.ProtectedForBinding(binding)
			if err != nil {
				t.Fatalf("protected binding %d: %v", binding.ID, err)
			}
			converged = append(converged, protected)
			return true, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewMonitorPipelineReconciler: %v", err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !reflect.DeepEqual(resolved, []int64{first.ID, second.ID}) {
		t.Fatalf("resolved bindings = %v", resolved)
	}
	if len(converged) != 2 {
		t.Fatalf("converged renders = %d", len(converged))
	}
	if converged[0].PipelineName == converged[1].PipelineName ||
		converged[0].ConfigHash == converged[1].ConfigHash {
		t.Fatal("binding identity was not domain-separated")
	}
}

func TestMonitorPipelineReconcilerFailsClosedBeforeConvergence(t *testing.T) {
	binding := monitorPipelineBinding()
	want := errors.New("policy unavailable")
	convergeCalled := false
	reconciler, err := pullrequest.NewMonitorPipelineReconciler(
		binding.TeamID,
		monitorBindingListerFunc(func(
			context.Context,
			int,
		) ([]pullrequest.Binding, error) {
			return []pullrequest.Binding{binding}, nil
		}),
		monitorPolicyResolverFunc(func(
			context.Context,
			pullrequest.Binding,
		) (pullrequest.MonitorPipelinePolicy, error) {
			return pullrequest.MonitorPipelinePolicy{}, want
		}),
		monitorPipelineConvergerFunc(func(
			context.Context,
			pullrequest.Binding,
			pullrequest.RenderedMonitorPipeline,
		) (bool, error) {
			convergeCalled = true
			return false, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewMonitorPipelineReconciler: %v", err)
	}
	if err := reconciler.Reconcile(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Reconcile error = %v, want %v", err, want)
	}
	if convergeCalled {
		t.Fatal("pipeline convergence ran without trusted policy")
	}
}

func monitorPipelineBinding() pullrequest.Binding {
	runID := snapshot.WorkflowRunID(41)
	observationID := snapshot.SnapshotID(51)
	return pullrequest.Binding{
		ID: 118, TeamID: 7,
		Locator: pullrequest.Locator{
			Provider: pullrequest.ProviderGitHub, Repository: "acme/widget",
			ExternalID: "118",
		},
		URL:       "https://github.example/acme/widget/pull/118",
		SourceRef: "refs/heads/feature/pr", TargetRef: "refs/heads/main",
		OriginatingWorkflowRunID:    &runID,
		MonitorWorkflowDefinitionID: 31, MonitorWorkflowVersion: 3,
		AcknowledgedCursor:        pullrequest.Cursor("cursor-1"),
		LastObservationSnapshotID: &observationID,
		LastReconciledSourceSHA:   strings.Repeat("b", 40),
		LastReconciledTargetSHA:   strings.Repeat("c", 40),
		LastReconciledAt:          time.Date(2026, time.July, 30, 12, 0, 0, 123000000, time.UTC),
		State:                     pullrequest.BindingActive,
		Active: &pullrequest.LaunchReservation{
			BindingID: 118, BindingRevision: 9, BaseRevision: 8,
			ActionDigest: "sha256:" + strings.Repeat("d", 64),
		},
		Revision: 9,
	}
}

type monitorBindingListerFunc func(
	context.Context,
	int,
) ([]pullrequest.Binding, error)

func (function monitorBindingListerFunc) ListActive(
	ctx context.Context,
	teamID int,
) ([]pullrequest.Binding, error) {
	return function(ctx, teamID)
}

type monitorPolicyResolverFunc func(
	context.Context,
	pullrequest.Binding,
) (pullrequest.MonitorPipelinePolicy, error)

func (function monitorPolicyResolverFunc) ResolveMonitorPipelinePolicy(
	ctx context.Context,
	binding pullrequest.Binding,
) (pullrequest.MonitorPipelinePolicy, error) {
	return function(ctx, binding)
}

type monitorPipelineConvergerFunc func(
	context.Context,
	pullrequest.Binding,
	pullrequest.RenderedMonitorPipeline,
) (bool, error)

func (function monitorPipelineConvergerFunc) ConvergeMonitorPipeline(
	ctx context.Context,
	binding pullrequest.Binding,
	rendered pullrequest.RenderedMonitorPipeline,
) (bool, error) {
	return function(ctx, binding, rendered)
}

func monitorPipelinePolicy() pullrequest.MonitorPipelinePolicy {
	return pullrequest.MonitorPipelinePolicy{
		APIBaseURL:        "https://api.github.example",
		RepositoryURL:     "https://github.example/acme/widget.git",
		ReadCredential:    "engineering-github-read",
		PollInterval:      5 * time.Minute,
		FreshnessInterval: 6 * time.Hour,
		ResourceType: atc.ResourceType{
			Name:  pullrequest.MonitorResourceTypeName,
			Image: "registry.example/forge-pr@sha256:" + strings.Repeat("a", 64),
		},
	}
}
