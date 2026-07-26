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
	if spec.TeamID != 7 || spec.TeamName != "main" || spec.Name != "agent-resource-capture-"+result.OperationKey[:24] || !spec.Config.Template {
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

	firstDigest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	secondDigest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	manifestFor := func(id snapshot.SnapshotID, digest snapshot.Digest) snapshot.Snapshot {
		return snapshot.Snapshot{
			ID: id, Type: snapshot.TypeRef("repository/v1"), Digest: digest,
			ByteSize: 4096, FileCount: 9, Representation: "application/x-tar",
			ContentState: snapshot.ContentStateAvailable, CreatedAt: repositoryResource().CapturedAt,
		}
	}

	// Generation 51 succeeded and was finalized once; its bytes then expired.
	// Generation 52 is the retry the platform binds under the SAME capture
	// identity, and it finalizes its own manifest.
	executions := &fakeExecutions{}
	executionCall := 0
	executions.start = func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		executionCall++
		switch executionCall {
		case 1, 2:
			if request.RetryPipelineRunID != 0 {
				t.Fatalf("initial execution requested a retry: %#v", request)
			}
			return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: 41, InstancePipelineID: 61, Status: db.PipelineRunSucceeded}, false, nil
		case 3:
			if request.RetryPipelineRunID != 51 {
				t.Fatalf("retry did not bind the expired generation: %#v", request)
			}
			return resourcecapture.Execution{PipelineRunID: 52, TemplatePipelineID: 41, InstancePipelineID: 62, Status: db.PipelineRunSucceeded}, true, nil
		default:
			t.Fatalf("unexpected execution call %d: %#v", executionCall, request)
			return resourcecapture.Execution{}, false, nil
		}
	}
	outputs := &fakeOutputs{}
	capturer := newCapturer(t, resolver, templates, executions, outputs)

	// The first Capture finalizes generation 51 normally.
	outputs.finalize = func(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		if request.PipelineRunID != 51 {
			t.Fatalf("unexpected finalize for run %d", request.PipelineRunID)
		}
		return manifestFor(71, firstDigest), true, nil
	}
	first, err := capturer.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot == nil || first.Snapshot.Digest != firstDigest {
		t.Fatalf("first capture = %#v", first)
	}

	// The second Capture finds generation 51's bytes gone and retries it.
	outputs.finalize = func(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		if request.PipelineRunID == 51 {
			return snapshot.Snapshot{}, false, resourcecapture.ErrOutputUnavailable
		}
		if request.PipelineRunID != 52 {
			t.Fatalf("unexpected finalize for run %d", request.PipelineRunID)
		}
		return manifestFor(72, secondDigest), true, nil
	}
	second, err := capturer.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}

	if second.Execution.PipelineRunID != 52 || !second.Created {
		t.Fatalf("retry result = %#v", second)
	}
	if second.Snapshot == nil {
		t.Fatalf("retry produced no snapshot: %#v", second)
	}
	if len(executions.calls) != 3 || len(outputs.calls) != 3 {
		t.Fatalf("retry calls = executions %d outputs %d", len(executions.calls), len(outputs.calls))
	}

	// Capture re-run is NOT byte-deterministic, and this is the assertion that
	// says so out loud. A git resource's output carries .git, whose index stores
	// per-file stat data and whose logs store wall-clock times; the canonicalizer
	// zeroes filesystem metadata but never rewrites file content, and git stores
	// its metadata as content. Observed 2026-07-25 by capturing two independent
	// checkouts of one commit through the real Canonicalizer.
	//
	// What IS stable is the capture identity. The retry binds a NEW digest under
	// the SAME operation key, and nothing in the capture path may compare the two
	// or refuse the mismatch — if a future change adds such a comparison, this
	// test is what fails.
	if second.Snapshot.Digest == first.Snapshot.Digest {
		t.Fatalf("the retry generation was expected to bind a new digest, got %s twice", second.Snapshot.Digest)
	}
	if second.OperationKey != first.OperationKey {
		t.Fatalf("re-capture changed the capture identity: %q != %q", second.OperationKey, first.OperationKey)
	}
	if second.Snapshot.ID == first.Snapshot.ID {
		t.Fatalf("the retry generation reused snapshot ID %s", second.Snapshot.ID)
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
