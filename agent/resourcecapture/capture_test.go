package resourcecapture_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type fakeResolver struct {
	resolve func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error)
	calls   []resourcecapture.ResolveRequest
}

func (fake *fakeResolver) Resolve(ctx context.Context, request resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
	fake.calls = append(fake.calls, request.Clone())
	if fake.resolve == nil {
		return resourcecapture.ResolvedResource{}, false, errors.New("unexpected resolve")
	}
	return fake.resolve(ctx, request)
}

type fakeTemplates struct {
	save  func(context.Context, resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error)
	calls []resourcecapture.TemplateSpec
}

func (fake *fakeTemplates) SaveOrReuse(ctx context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
	fake.calls = append(fake.calls, spec.Clone())
	if fake.save == nil {
		return resourcecapture.TemplateRef{}, errors.New("unexpected template save")
	}
	return fake.save(ctx, spec)
}

type fakeExecutions struct {
	start func(context.Context, resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error)
	calls []resourcecapture.ExecutionRequest
}

func (fake *fakeExecutions) StartOrGet(ctx context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
	fake.calls = append(fake.calls, request)
	if fake.start == nil {
		return resourcecapture.Execution{}, false, errors.New("unexpected execution")
	}
	return fake.start(ctx, request)
}

type fakeOutputs struct {
	finalize func(context.Context, resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error)
	calls    []resourcecapture.OutputRequest
}

func (fake *fakeOutputs) Finalize(ctx context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
	fake.calls = append(fake.calls, request)
	if fake.finalize == nil {
		return snapshot.Snapshot{}, false, errors.New("unexpected output finalization")
	}
	return fake.finalize(ctx, request)
}

func TestCaptureBuildsExactPinnedGetAndTypedPassThrough(t *testing.T) {
	resolved := repositoryResource()
	resolver := &fakeResolver{resolve: func(_ context.Context, request resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		if request.TeamID != 7 || request.TeamName != "main" || request.Pipeline.Name != "delivery" ||
			!reflect.DeepEqual(request.Pipeline.InstanceVars, atc.InstanceVars{"branch": "main"}) ||
			request.Resource != "repository" || !reflect.DeepEqual(request.Version, atc.Version{"ref": "abc123"}) {
			t.Fatalf("resolve request = %#v", request)
		}
		return resolved, true, nil
	}}
	templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 41, Name: spec.Name}, nil
	}}
	executions := &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		return resourcecapture.Execution{
			PipelineRunID: 51, TemplatePipelineID: request.Template.ID,
			InstancePipelineID: 61, Status: db.PipelineRunRunning,
		}, true, nil
	}}
	outputs := &fakeOutputs{}
	capturer := newCapturer(t, resolver, templates, executions, outputs)

	result, err := capturer.Capture(context.Background(), resourcecapture.Request{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{
			Name: "delivery", InstanceVars: atc.InstanceVars{"branch": "main"},
		},
		Resource: "repository", Version: atc.Version{"ref": "abc123"},
		CreatedBy: "Alice", Actor: "github:subject-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Snapshot != nil || result.Execution.PipelineRunID != 51 || result.OperationKey == "" {
		t.Fatalf("capture result = %#v", result)
	}
	if len(templates.calls) != 1 || len(executions.calls) != 1 || len(outputs.calls) != 0 {
		t.Fatalf("calls templates=%d executions=%d outputs=%d", len(templates.calls), len(executions.calls), len(outputs.calls))
	}

	spec := templates.calls[0]
	targetHash, err := workflow.TargetConfigHash(spec.Config)
	if err != nil {
		t.Fatal(err)
	}
	wantName := "agent-resource-capture-" + result.OperationKey[:24] + "-" + targetHash[:12]
	if spec.TeamID != 7 || spec.TeamName != "main" || spec.Name != wantName || !spec.Config.Template {
		t.Fatalf("template identity = %#v", spec)
	}
	if len(spec.Config.Resources) != 1 || !reflect.DeepEqual(spec.Config.Resources[0], resolved.Resource) {
		t.Fatalf("template resources = %#v", spec.Config.Resources)
	}
	if !reflect.DeepEqual(spec.Config.ResourceTypes, resolved.ResourceTypes) || len(spec.Config.Jobs) != 1 {
		t.Fatalf("template dependencies/jobs = %#v / %#v", spec.Config.ResourceTypes, spec.Config.Jobs)
	}
	plan := spec.Config.Jobs[0].PlanSequence
	if len(plan) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	get, ok := plan[0].Config.(*atc.GetStep)
	if !ok || get.Name != "source" || get.Resource != "repository" || get.Trigger || get.SkipDownload ||
		get.Version == nil || !reflect.DeepEqual(get.Version.Pinned, atc.Version{"ref": "abc123"}) {
		t.Fatalf("pinned get = %#v", plan[0].Config)
	}
	task, ok := plan[1].Config.(*atc.TaskStep)
	if !ok || task.Name != "seal-snapshot" || !task.Hermetic || task.Config == nil {
		t.Fatalf("pass-through task = %#v", plan[1].Config)
	}
	if task.Config.Run.Path != "/bin/sh" || !reflect.DeepEqual(task.Config.Run.Args, []string{"-ec", `cp -a source/. snapshot/`}) ||
		!reflect.DeepEqual(task.Config.Inputs, []atc.TaskInputConfig{{Name: "source"}}) ||
		!reflect.DeepEqual(task.Config.Outputs, []atc.TaskOutputConfig{{Name: "snapshot"}}) {
		t.Fatalf("pass-through config = %#v", task.Config)
	}
	if task.Config.ImageResource == nil || task.Config.ImageResource.Type != "registry-image" ||
		task.Config.ImageResource.Source["repository"] != "ghcr.io/acme/agent-runner" ||
		task.Config.ImageResource.Version["digest"] != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("pass-through image = %#v", task.Config.ImageResource)
	}
	declaration, found := task.SnapshotOutputs["snapshot"]
	if !found || declaration.Type != snapshot.TypeRef("repository/v1") || declaration.Retention != snapshot.RetentionClassBinding {
		t.Fatalf("snapshot declaration = %#v", declaration)
	}
	var metadata resourcecapture.SourceMetadata
	if err := json.Unmarshal(declaration.SourceMetadata, &metadata); err != nil {
		t.Fatalf("source metadata: %v", err)
	}
	if metadata.Adapter != resourcecapture.AdapterResourceVersion || metadata.OperationKey != result.OperationKey ||
		metadata.Pipeline != "delivery" || metadata.Resource != "repository" || metadata.ResourceType != "git" ||
		metadata.ResourceConfigVersionID != 31 || metadata.SnapshotType != snapshot.TypeRef("repository/v1") ||
		!reflect.DeepEqual(metadata.Version, atc.Version{"ref": "abc123"}) {
		t.Fatalf("source metadata = %#v", metadata)
	}
	if strings.Contains(string(declaration.SourceMetadata), "literal-secret") {
		t.Fatalf("source credentials leaked into production metadata: %s", declaration.SourceMetadata)
	}
	if executions.calls[0].OperationKey != result.OperationKey || executions.calls[0].Template.ID != 41 || executions.calls[0].CreatedBy != "Alice" {
		t.Fatalf("execution request = %#v", executions.calls[0])
	}
}

func TestCaptureTemplateIdentityTracksRenderedConfigAndIsStable(t *testing.T) {
	newRunningCapture := func(t *testing.T, resolved resourcecapture.ResolvedResource, image string) (*resourcecapture.Capturer, *fakeTemplates) {
		t.Helper()
		resolver := &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
			return resolved, true, nil
		}}
		templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
			return resourcecapture.TemplateRef{ID: 41, Name: spec.Name}, nil
		}}
		executions := &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
			return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: request.Template.ID, InstancePipelineID: 61, Status: db.PipelineRunRunning}, true, nil
		}}
		capturer, err := resourcecapture.NewCapturer(resolver, templates, executions, &fakeOutputs{}, image)
		if err != nil {
			t.Fatal(err)
		}
		return capturer, templates
	}
	assertCapturedIdentity := func(t *testing.T, result resourcecapture.CaptureResult, spec resourcecapture.TemplateSpec) {
		t.Helper()
		targetHash, err := workflow.TargetConfigHash(spec.Config)
		if err != nil {
			t.Fatal(err)
		}
		want := "agent-resource-capture-" + result.OperationKey[:24] + "-" + targetHash[:12]
		if spec.Name != want {
			t.Fatalf("template name = %q, want %q", spec.Name, want)
		}
	}

	baseImage := "ghcr.io/acme/agent-runner@sha256:" + strings.Repeat("a", 64)
	base, baseTemplates := newRunningCapture(t, repositoryResource(), baseImage)
	first, err := base.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := base.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(baseTemplates.calls) != 2 || first.OperationKey != second.OperationKey || baseTemplates.calls[0].Name != baseTemplates.calls[1].Name {
		t.Fatalf("same capture did not reuse identity: results=%#v/%#v specs=%#v", first, second, baseTemplates.calls)
	}
	assertCapturedIdentity(t, first, baseTemplates.calls[0])

	otherImage, otherImageTemplates := newRunningCapture(t, repositoryResource(), "ghcr.io/acme/agent-runner@sha256:"+strings.Repeat("b", 64))
	imageResult, err := otherImage.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertCapturedIdentity(t, imageResult, otherImageTemplates.calls[0])
	if imageResult.OperationKey == first.OperationKey || otherImageTemplates.calls[0].Name == baseTemplates.calls[0].Name {
		t.Fatalf("task image change did not change operation/template identity: %#v / %#v", first, imageResult)
	}

	changedResource := repositoryResource()
	changedResource.Resource.Source["uri"] = "git@example.invalid:acme/other.git"
	otherResource, otherResourceTemplates := newRunningCapture(t, changedResource, baseImage)
	resourceResult, err := otherResource.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertCapturedIdentity(t, resourceResult, otherResourceTemplates.calls[0])
	if resourceResult.OperationKey == first.OperationKey || otherResourceTemplates.calls[0].Name == baseTemplates.calls[0].Name {
		t.Fatalf("resource change did not change operation/template identity: %#v / %#v", first, resourceResult)
	}
}

func TestCaptureFinalizesSuccessfulOutputAndIsIdempotentAcrossMapOrder(t *testing.T) {
	resolver := &fakeResolver{resolve: func(_ context.Context, _ resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return repositoryResource(), true, nil
	}}
	templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 41, Name: spec.Name}, nil
	}}
	executions := &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: request.Template.ID, InstancePipelineID: 61, Status: db.PipelineRunSucceeded}, false, nil
	}}
	manifest := snapshot.Snapshot{
		ID: 71, Type: snapshot.TypeRef("repository/v1"), Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		ByteSize: 123, FileCount: 7, Representation: "application/x-tar",
		ContentState: snapshot.ContentStateAvailable,
		CreatedAt:    repositoryResource().CapturedAt,
	}
	outputs := &fakeOutputs{finalize: func(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		if request.TeamID != 7 || request.PipelineRunID != 51 || request.OutputPort != "snapshot" ||
			request.Actor != "github:subject-1" || request.ExpectedType != snapshot.TypeRef("repository/v1") || request.OperationKey == "" {
			t.Fatalf("output request = %#v", request)
		}
		return manifest, true, nil
	}}
	capturer := newCapturer(t, resolver, templates, executions, outputs)
	first := resourcecapture.Request{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{Name: "delivery"}, Resource: "repository",
		Version: atc.Version{"ref": "abc123", "digest": "sha256:one"}, Type: snapshot.TypeRef("repository/v1"),
		CreatedBy: "Alice", Actor: "github:subject-1",
	}
	second := first
	second.Version = atc.Version{"digest": "sha256:one", "ref": "abc123"}

	firstResult, err := capturer.Capture(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := capturer.Capture(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.OperationKey != secondResult.OperationKey || firstResult.Created || secondResult.Created ||
		firstResult.Snapshot == nil || firstResult.Snapshot.ID != manifest.ID || secondResult.Snapshot == nil || secondResult.Snapshot.ID != manifest.ID {
		t.Fatalf("idempotent results = %#v / %#v", firstResult, secondResult)
	}
	if len(outputs.calls) != 2 || outputs.calls[0].OperationKey != outputs.calls[1].OperationKey {
		t.Fatalf("output calls = %#v", outputs.calls)
	}
}

func TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired(t *testing.T) {
	resolver := &fakeResolver{resolve: func(_ context.Context, _ resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return repositoryResource(), true, nil
	}}
	templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: spec.Name, ConfigVersion: 2, FullHash: spec.FullHash}, nil
	}}
	executions := &fakeExecutions{}
	call := 0
	executions.start = func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		call++
		if call == 1 {
			if request.RetryPipelineRunID != 0 {
				t.Fatalf("initial execution requested retry: %#v", request)
			}
			return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: 41, InstancePipelineID: 61, Status: db.PipelineRunSucceeded}, false, nil
		}
		if request.RetryPipelineRunID != 51 {
			t.Fatalf("retry did not bind the expired generation: %#v", request)
		}
		return resourcecapture.Execution{PipelineRunID: 52, TemplatePipelineID: 41, InstancePipelineID: 62, Status: db.PipelineRunRunning}, true, nil
	}
	outputs := &fakeOutputs{finalize: func(context.Context, resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		return snapshot.Snapshot{}, false, resourcecapture.ErrOutputUnavailable
	}}
	capturer := newCapturer(t, resolver, templates, executions, outputs)

	result, err := capturer.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Snapshot != nil || result.Execution.PipelineRunID != 52 || result.Execution.Status != db.PipelineRunRunning {
		t.Fatalf("retry result = %#v", result)
	}
	if len(executions.calls) != 2 || len(outputs.calls) != 1 {
		t.Fatalf("retry calls = executions %d outputs %d", len(executions.calls), len(outputs.calls))
	}
}

func TestCaptureRejectsMissingDisabledAndAmbiguousTypesBeforeExecution(t *testing.T) {
	tests := map[string]struct {
		request  resourcecapture.Request
		resolved resourcecapture.ResolvedResource
		found    bool
		want     error
	}{
		"version not found": {
			request: validRequest(), resolved: repositoryResource(), found: false, want: resourcecapture.ErrNotFound,
		},
		"disabled version": {
			request: validRequest(), resolved: func() resourcecapture.ResolvedResource {
				value := repositoryResource()
				value.Enabled = false
				return value
			}(),
			found: true, want: resourcecapture.ErrDisabled,
		},
		"non-git needs type": {
			request: validRequest(), resolved: func() resourcecapture.ResolvedResource {
				value := repositoryResource()
				value.Resource.Type = "s3"
				return value
			}(),
			found: true, want: resourcecapture.ErrTypeRequired,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &fakeResolver{resolve: func(_ context.Context, _ resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
				return test.resolved, test.found, nil
			}}
			templates := &fakeTemplates{}
			executions := &fakeExecutions{}
			outputs := &fakeOutputs{}
			capturer := newCapturer(t, resolver, templates, executions, outputs)
			_, err := capturer.Capture(context.Background(), test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Capture() error = %v, want %v", err, test.want)
			}
			if len(templates.calls) != 0 || len(executions.calls) != 0 || len(outputs.calls) != 0 {
				t.Fatalf("side effects after rejection: templates=%d executions=%d outputs=%d", len(templates.calls), len(executions.calls), len(outputs.calls))
			}
		})
	}
}

func TestCapturePropagatesCancellationWithoutAdvancing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := &fakeResolver{resolve: func(ctx context.Context, _ resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return resourcecapture.ResolvedResource{}, false, ctx.Err()
	}}
	templates := &fakeTemplates{}
	executions := &fakeExecutions{}
	outputs := &fakeOutputs{}
	capturer := newCapturer(t, resolver, templates, executions, outputs)
	_, err := capturer.Capture(ctx, validRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Capture() error = %v, want context cancellation", err)
	}
	if len(templates.calls) != 0 || len(executions.calls) != 0 || len(outputs.calls) != 0 {
		t.Fatal("canceled capture advanced past resolution")
	}
}

func TestCaptureRejectsMutableTaskImageBeforeCreatingTemplate(t *testing.T) {
	resolver := &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return repositoryResource(), true, nil
	}}
	templates := &fakeTemplates{}
	executions := &fakeExecutions{}
	outputs := &fakeOutputs{}
	capturer, err := resourcecapture.NewCapturer(
		resolver, templates, executions, outputs, "ghcr.io/acme/agent-runner:v1.2.3",
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = capturer.Capture(context.Background(), validRequest())
	if !errors.Is(err, resourcecapture.ErrUnavailable) || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Capture() error = %v, want immutable image refusal", err)
	}
	if len(templates.calls) != 0 || len(executions.calls) != 0 || len(outputs.calls) != 0 {
		t.Fatal("mutable task image advanced to a side effect")
	}
}

func newCapturer(t *testing.T, resolver resourcecapture.Resolver, templates resourcecapture.TemplateStore, executions resourcecapture.ExecutionStore, outputs resourcecapture.OutputStore) *resourcecapture.Capturer {
	t.Helper()
	capturer, err := resourcecapture.NewCapturer(resolver, templates, executions, outputs, "ghcr.io/acme/agent-runner@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return capturer
}

func validRequest() resourcecapture.Request {
	return resourcecapture.Request{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{Name: "delivery"}, Resource: "repository",
		Version: atc.Version{"ref": "abc123"}, CreatedBy: "Alice", Actor: "github:subject-1",
	}
}

func repositoryResource() resourcecapture.ResolvedResource {
	return resourcecapture.ResolvedResource{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{
			Name: "delivery", InstanceVars: atc.InstanceVars{"branch": "main"},
		},
		PipelineConfigVersion: 11,
		Resource: atc.ResourceConfig{
			Name: "repository", Type: "git",
			Source: atc.Source{"uri": "git@example.invalid:acme/repo.git", "private_key": "literal-secret"},
		},
		ResourceTypes:           atc.ResourceTypes{{Name: "git", Type: "registry-image", Source: atc.Source{"repository": "acme/git-resource"}}},
		ResourceConfigVersionID: 31, Version: atc.Version{"ref": "abc123"}, Enabled: true,
		CapturedAt: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	}
}
