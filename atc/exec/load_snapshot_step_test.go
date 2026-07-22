package exec_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/stretchr/testify/require"
)

type bindingVerifierFunc func(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error)

func (function bindingVerifierFunc) InputBindingMatches(ctx context.Context, teamID, buildID int, runID snapshot.WorkflowRunID, port string, ref *snapshot.SnapshotRef) (bool, error) {
	return function(ctx, teamID, buildID, runID, port, ref)
}

func TestLoadSnapshotStepAuthorizesBindsAndRegistersAtomically(t *testing.T) {
	manifest := loadSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := new(snapshotfakes.FakeContentStore)
	content.OpenReturns(io.NopCloser(strings.NewReader("unused")), nil)

	verifierCalls := 0
	verifier := bindingVerifierFunc(func(_ context.Context, teamID, buildID int, runID snapshot.WorkflowRunID, port string, ref *snapshot.SnapshotRef) (bool, error) {
		verifierCalls++
		require.Equal(t, 17, teamID)
		require.Equal(t, 42, buildID)
		require.Equal(t, snapshot.WorkflowRunID(9007199254740993), runID)
		require.Equal(t, "subject", port)
		require.Equal(t, snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}, *ref)
		return true, nil
	})

	repository, state, delegateFactory, delegate := loadSnapshotHarness()
	step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: manifest.ID.String(), Type: manifest.Type,
		WorkflowRunID: "9007199254740993",
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, verifier)

	ok, err := step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, metadata.GetAuthorizedCallCount())
	require.Equal(t, 1, verifierCalls)
	require.Equal(t, 1, delegate.FinishedCallCount())
	entry, found := repository.ArtifactEntryFor("subject")
	require.True(t, found)
	require.False(t, entry.FromCache)
	require.Equal(t, &snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}, entry.Snapshot)
	require.Equal(t, "snapshot:"+manifest.ID.String(), entry.Artifact.Handle())
}

func TestLoadSnapshotStepOptionalZeroIsNoopButCanVerifyUnboundWorkflowInput(t *testing.T) {
	metadata := new(snapshotfakes.FakeMetadataStore)
	content := new(snapshotfakes.FakeContentStore)
	calls := 0
	verifier := bindingVerifierFunc(func(_ context.Context, teamID, buildID int, runID snapshot.WorkflowRunID, port string, ref *snapshot.SnapshotRef) (bool, error) {
		calls++
		require.Nil(t, ref)
		return true, nil
	})
	repository, state, delegateFactory, _ := loadSnapshotHarness()
	step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "optional", ID: "0", Type: snapshot.TypeRef("review/v1"), Optional: true,
		WorkflowRunID: "9007199254740993",
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, verifier)

	ok, err := step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, calls)
	require.Zero(t, metadata.GetAuthorizedCallCount())
	require.Zero(t, content.OpenCallCount())
	_, found := repository.ArtifactEntryFor("optional")
	require.False(t, found)
}

func TestLoadSnapshotStepFailsClosedWithoutLeakingMissingIdentity(t *testing.T) {
	for _, id := range []string{"1", "2"} {
		metadata := new(snapshotfakes.FakeMetadataStore)
		metadata.GetAuthorizedReturns(snapshot.Snapshot{}, false, nil)
		_, state, delegateFactory, _ := loadSnapshotHarness()
		step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
			Name: "subject", ID: id, Type: snapshot.TypeRef("review/v1"),
		}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, new(snapshotfakes.FakeContentStore), nil)
		ok, err := step.Run(context.Background(), state)
		require.False(t, ok)
		var unavailable exec.SnapshotUnavailableError
		require.True(t, errors.As(err, &unavailable))
		require.Equal(t, "snapshot unavailable or unauthorized", err.Error())
		require.NotContains(t, err.Error(), id)
		require.Empty(t, state.ArtifactRepository().AsMap())
	}

	_, state, delegateFactory, _ := loadSnapshotHarness()
	disabled := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: "1", Type: snapshot.TypeRef("review/v1"),
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, nil, nil, nil)
	ok, err := disabled.Run(context.Background(), state)
	require.False(t, ok)
	require.ErrorContains(t, err, "snapshot loading is disabled")
	require.Empty(t, state.ArtifactRepository().AsMap())

	for _, identity := range []exec.StepMetadata{{TeamID: 0, BuildID: 42}, {TeamID: 17, BuildID: 0}} {
		metadata := new(snapshotfakes.FakeMetadataStore)
		_, state, delegateFactory, _ := loadSnapshotHarness()
		step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
			Name: "subject", ID: "1", Type: snapshot.TypeRef("review/v1"),
		}, identity, delegateFactory, metadata, new(snapshotfakes.FakeContentStore), nil)
		ok, err := step.Run(context.Background(), state)
		require.False(t, ok)
		require.EqualError(t, err, "load_snapshot: server build identity is unavailable")
		require.Zero(t, metadata.GetAuthorizedCallCount())
	}
}

func TestLoadSnapshotStepRedactsDependencyFailures(t *testing.T) {
	sensitive := errors.New("query snapshot id 123 at https://secret-node/internal")
	manifest := loadSnapshotManifest()

	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(snapshot.Snapshot{}, false, sensitive)
	_, state, delegateFactory, _ := loadSnapshotHarness()
	step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: manifest.ID.String(), Type: manifest.Type,
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, new(snapshotfakes.FakeContentStore), nil)
	_, err := step.Run(context.Background(), state)
	require.EqualError(t, err, "load_snapshot: metadata authorization failed")
	require.NotContains(t, err.Error(), "123")

	metadata.GetAuthorizedReturns(manifest, true, nil)
	step = exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: manifest.ID.String(), Type: manifest.Type, WorkflowRunID: "1",
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, new(snapshotfakes.FakeContentStore),
		bindingVerifierFunc(func(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error) {
			return false, sensitive
		}))
	_, err = step.Run(context.Background(), state)
	require.EqualError(t, err, "load_snapshot: workflow input binding verification failed")
	require.NotContains(t, err.Error(), "secret")
}

func TestLoadSnapshotStepRejectsInvalidPlansAndAuthorizedManifestsBeforePublication(t *testing.T) {
	valid := loadSnapshotManifest()
	for _, test := range []struct {
		name     string
		plan     atc.LoadSnapshotPlan
		manifest snapshot.Snapshot
	}{
		{name: "required zero", plan: atc.LoadSnapshotPlan{Name: "subject", ID: "0", Type: valid.Type}, manifest: valid},
		{name: "unresolved id", plan: atc.LoadSnapshotPlan{Name: "subject", ID: "((subject_id))", Type: valid.Type}, manifest: valid},
		{name: "wrong manifest id", plan: atc.LoadSnapshotPlan{Name: "subject", ID: valid.ID.String(), Type: valid.Type}, manifest: func() snapshot.Snapshot { value := valid; value.ID++; return value }()},
		{name: "invalid manifest", plan: atc.LoadSnapshotPlan{Name: "subject", ID: valid.ID.String(), Type: valid.Type}, manifest: func() snapshot.Snapshot { value := valid; value.CreatedAt = time.Time{}; return value }()},
		{name: "expired manifest", plan: atc.LoadSnapshotPlan{Name: "subject", ID: valid.ID.String(), Type: valid.Type}, manifest: func() snapshot.Snapshot {
			value := valid
			value.ContentState = snapshot.ContentStateExpired
			return value
		}()},
		{name: "wrong type", plan: atc.LoadSnapshotPlan{Name: "subject", ID: valid.ID.String(), Type: "repository/v1"}, manifest: valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := new(snapshotfakes.FakeMetadataStore)
			metadata.GetAuthorizedReturns(test.manifest, true, nil)
			content := new(snapshotfakes.FakeContentStore)
			repository, state, delegateFactory, _ := loadSnapshotHarness()
			step := exec.NewLoadSnapshotStep("9", test.plan, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, nil)
			ok, err := step.Run(context.Background(), state)
			require.False(t, ok)
			require.Error(t, err)
			require.Empty(t, repository.AsMap())
			require.Zero(t, content.OpenCallCount())
		})
	}
}

func TestLoadSnapshotStepWithoutWorkflowSkipsBindingVerifier(t *testing.T) {
	manifest := loadSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	called := false
	verifier := bindingVerifierFunc(func(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error) {
		called = true
		return false, errors.New("must not be called")
	})
	_, state, delegateFactory, _ := loadSnapshotHarness()
	step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: manifest.ID.String(), Type: manifest.Type,
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, new(snapshotfakes.FakeContentStore), verifier)
	ok, err := step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, called)
}

func TestLoadSnapshotStepPropagatesRepositoryScopeFailureWithoutPartialEntry(t *testing.T) {
	manifest := loadSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	parent := build.NewRepository()
	closed := parent.NewLocalScope()
	require.NoError(t, closed.CommitToParent())
	state := new(execfakes.FakeRunState)
	state.ArtifactRepositoryReturns(closed)
	delegate := new(execfakes.FakeBuildStepDelegate)
	delegate.StartSpanReturns(context.Background(), tracing.NoopSpan)
	delegateFactory := new(execfakes.FakeBuildStepDelegateFactory)
	delegateFactory.BuildStepDelegateReturns(delegate)
	step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
		Name: "subject", ID: manifest.ID.String(), Type: manifest.Type,
	}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, new(snapshotfakes.FakeContentStore), nil)
	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "load_snapshot: artifact publication failed")
	require.Empty(t, closed.AsMap())
	require.Empty(t, parent.AsMap())
}

func TestLoadSnapshotStepResumeRequiresTrustedSnapshotArtifactIdentity(t *testing.T) {
	manifest := loadSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := new(snapshotfakes.FakeContentStore)
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}

	t.Run("trusted identical adapter is idempotent", func(t *testing.T) {
		repository, state, delegateFactory, _ := loadSnapshotHarness()
		artifact, err := runtime.NewSnapshotArtifact(manifest, content)
		require.NoError(t, err)
		require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"subject": {Artifact: artifact, Snapshot: &ref},
		}))
		step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{Name: "subject", ID: manifest.ID.String(), Type: manifest.Type},
			exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, nil)
		ok, err := step.Run(context.Background(), state)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("string-spoofed adapter conflicts", func(t *testing.T) {
		repository, state, delegateFactory, _ := loadSnapshotHarness()
		require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"subject": {Artifact: &loadArtifact{handle: "snapshot:" + manifest.ID.String()}, Snapshot: &ref},
		}))
		step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{Name: "subject", ID: manifest.ID.String(), Type: manifest.Type},
			exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, nil)
		ok, err := step.Run(context.Background(), state)
		require.False(t, ok)
		require.ErrorContains(t, err, "already produced")
	})
}

func TestLoadSnapshotStepRejectsBindingAndRepositoryConflicts(t *testing.T) {
	manifest := loadSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := new(snapshotfakes.FakeContentStore)

	for _, test := range []struct {
		name     string
		prepare  func(*build.Repository)
		verifier bindingVerifierFunc
	}{
		{
			name: "binding mismatch",
			verifier: func(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error) {
				return false, nil
			},
		},
		{
			name: "existing producer conflict",
			prepare: func(repository *build.Repository) {
				repository.RegisterArtifact("subject", &loadArtifact{handle: "worker:other"}, false)
			},
			verifier: func(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error) {
				return true, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, state, delegateFactory, _ := loadSnapshotHarness()
			if test.prepare != nil {
				test.prepare(repository)
			}
			step := exec.NewLoadSnapshotStep("9", atc.LoadSnapshotPlan{
				Name: "subject", ID: manifest.ID.String(), Type: manifest.Type,
				WorkflowRunID: "9007199254740993",
			}, exec.StepMetadata{TeamID: 17, BuildID: 42}, delegateFactory, metadata, content, test.verifier)
			ok, err := step.Run(context.Background(), state)
			require.False(t, ok)
			require.Error(t, err)
			entry, found := repository.ArtifactEntryFor("subject")
			if test.prepare == nil {
				require.False(t, found)
			} else {
				require.Equal(t, "worker:other", entry.Artifact.Handle())
			}
		})
	}
}

func loadSnapshotHarness() (*build.Repository, *execfakes.FakeRunState, *execfakes.FakeBuildStepDelegateFactory, *execfakes.FakeBuildStepDelegate) {
	repository := build.NewRepository()
	state := new(execfakes.FakeRunState)
	state.ArtifactRepositoryReturns(repository)
	delegate := new(execfakes.FakeBuildStepDelegate)
	delegate.StartSpanReturns(context.Background(), tracing.NoopSpan)
	delegateFactory := new(execfakes.FakeBuildStepDelegateFactory)
	delegateFactory.BuildStepDelegateReturns(delegate)
	return repository, state, delegateFactory, delegate
}

func loadSnapshotManifest() snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: snapshot.SnapshotID(9007199254740993), Type: snapshot.TypeRef("review/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)), ByteSize: 1024,
		FileCount: 1, Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}
}

type loadArtifact struct{ handle string }

func (artifact *loadArtifact) StreamOut(context.Context, string, compression.Compression) (io.ReadCloser, error) {
	panic("unused")
}
func (artifact *loadArtifact) Handle() string { return artifact.handle }
func (artifact *loadArtifact) Source() string { return "worker" }

var _ runtime.Artifact = (*loadArtifact)(nil)
