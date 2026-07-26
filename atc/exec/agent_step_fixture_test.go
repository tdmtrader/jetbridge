package exec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/onsi/gomega/gbytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fixtureOversizeBytes sizes the hostile-oversized fixture payload. It is
// deliberately NOT fixtureagent's own default: passing the default would leave
// the Authority.OversizeBytes plumbing without a witness, since a fixture that
// ignored the field entirely would emit a byte-identical tar. Any value
// comfortably above the archive-layer MaxContentBytes the hostile specs inject
// works.
const fixtureOversizeBytes = 2048

// fixtureTarVolume is a runtime.Volume whose StreamOut hands back fixed raw tar
// bytes. It exists because the production OpenTar passes a nil
// compression.Compression (raw), which runtimetest.Volume cannot serve, and
// because an fstest.MapFS cannot express a `../` entry name or a symlink — the
// two archive-layer attacks this suite must reach.
type fixtureTarVolume struct {
	*runtimetest.Volume
	tar []byte
}

func newFixtureTarVolume(handle string, raw []byte) *fixtureTarVolume {
	return &fixtureTarVolume{Volume: runtimetest.NewVolume(handle), tar: raw}
}

func (v *fixtureTarVolume) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(v.tar)), nil
}

// fixtureMemoryContent is a snapshot.ContentStore over a map. It verifies the
// digest on write exactly like the durable store does, so a canonicalization
// bug cannot hide behind a cooperative fake.
type fixtureMemoryContent struct {
	mutex   sync.Mutex
	objects map[snapshot.Digest][]byte
}

func newFixtureMemoryContent() *fixtureMemoryContent {
	return &fixtureMemoryContent{objects: map[snapshot.Digest][]byte{}}
}

func (store *fixtureMemoryContent) Put(_ context.Context, digest snapshot.Digest, reader io.Reader) ([]snapshot.Location, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if snapshot.Digest("sha256:"+hex.EncodeToString(sum[:])) != digest {
		return nil, fixtureDigestMismatch
	}
	store.mutex.Lock()
	store.objects[digest] = append([]byte(nil), body...)
	store.mutex.Unlock()
	return []snapshot.Location{{Digest: digest, Driver: "fixture-memory", Key: digest.String()}}, nil
}

func (store *fixtureMemoryContent) Open(_ context.Context, value snapshot.Snapshot) (io.ReadCloser, error) {
	store.mutex.Lock()
	body, found := store.objects[value.Digest]
	store.mutex.Unlock()
	if !found {
		return nil, fixtureContentMissing
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), body...))), nil
}

func (store *fixtureMemoryContent) Exists(_ context.Context, location snapshot.Location) (bool, error) {
	store.mutex.Lock()
	_, found := store.objects[location.Digest]
	store.mutex.Unlock()
	return found, nil
}

func (store *fixtureMemoryContent) DeleteLocation(_ context.Context, location snapshot.Location) error {
	store.mutex.Lock()
	delete(store.objects, location.Digest)
	store.mutex.Unlock()
	return nil
}

func (store *fixtureMemoryContent) DeleteAll(_ context.Context, digest snapshot.Digest) error {
	store.mutex.Lock()
	delete(store.objects, digest)
	store.mutex.Unlock()
	return nil
}

var (
	fixtureDigestMismatch = errors.New("fixture content store: digest mismatch")
	fixtureContentMissing = errors.New("fixture content store: content not found")
)

// fixtureMemoryMetadata implements only the four snapshot.MetadataStore methods
// BatchSealer.Seal and materializeSealedSnapshotArtifact call. Embedding the
// interface leaves the rest nil, so an unexpected call panics loudly instead of
// silently succeeding.
type fixtureMemoryMetadata struct {
	snapshot.MetadataStore

	mutex     sync.Mutex
	nextID    int64
	stages    int64
	committed map[snapshot.SnapshotID]snapshot.Snapshot
}

func newFixtureMemoryMetadata() *fixtureMemoryMetadata {
	return &fixtureMemoryMetadata{nextID: 900, committed: map[snapshot.SnapshotID]snapshot.Snapshot{}}
}

func (store *fixtureMemoryMetadata) StageUpload(
	_ context.Context, _ snapshot.DigestLease, request snapshot.StageUploadRequest,
) (snapshot.StagedUpload, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.stages++
	return snapshot.StagedUpload{
		ID: store.stages, Digest: request.Digest, TeamID: request.TeamID,
		Attempt: request.Attempt, LeaseExpiresAt: request.LeaseExpiresAt,
		CreatedAt: request.LeaseExpiresAt.Add(-time.Hour),
	}, nil
}

func (store *fixtureMemoryMetadata) DigestState(
	_ context.Context, _ snapshot.DigestLease, digest snapshot.Digest, _ time.Time,
) (snapshot.DigestState, error) {
	return snapshot.DigestState{Digest: digest}, nil
}

func (store *fixtureMemoryMetadata) CommitSealBatch(
	_ context.Context, _ snapshot.DigestLease, commit snapshot.SealCommit,
) (map[string]snapshot.SealedOutput, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	sealed := make(map[string]snapshot.SealedOutput, len(commit.Outputs))
	for _, output := range commit.Outputs {
		store.nextID++
		id := snapshot.SnapshotID(store.nextID)
		store.committed[id] = snapshot.Snapshot{
			ID: id, Type: output.Port.Type, Digest: output.Digest,
			ByteSize: output.ByteSize, FileCount: output.FileCount,
			Representation: output.Representation, IntrinsicMetadata: output.IntrinsicMetadata,
			ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
		}
		sealed[output.ClientKey] = snapshot.SealedOutput{
			Port:     output.Port,
			Snapshot: snapshot.SnapshotRef{ID: id, Type: output.Port.Type, Digest: output.Digest},
		}
	}
	return sealed, nil
}

func (store *fixtureMemoryMetadata) GetAuthorized(
	_ context.Context, _ int, id snapshot.SnapshotID,
) (snapshot.Snapshot, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	manifest, found := store.committed[id]
	return manifest, found, nil
}

// fixtureLease covers everything: the exec specs are single-threaded, and the
// lock manager's real exclusion semantics are WS6's subject, not this suite's.
type fixtureLease struct{ closed int }

func (l *fixtureLease) Covers(snapshot.Digest) bool { return true }
func (l *fixtureLease) Close() error                { l.closed++; return nil }

type fixtureLocks struct{ lease *fixtureLease }

func (l *fixtureLocks) AcquireMany(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) {
	if l.lease == nil {
		l.lease = &fixtureLease{}
	}
	return l.lease, nil
}

var _ = Describe("AgentStep against the real sealer (fixture agent)", func() {
	var (
		ctx    context.Context
		cancel func()

		fixtureCase   string
		contentLimit  int64
		markerFiles   runtimetest.VolumeContent
		inputRef      snapshot.SnapshotRef
		reviewSchema  snapshot.Digest
		outputVolume  *fixtureTarVolume
		metadataStore *fixtureMemoryMetadata
		contentStore  *fixtureMemoryContent
		locks         *fixtureLocks

		fakePool            *execfakes.FakePool
		fakeStreamer        *execfakes.FakeStreamer
		fakeDelegate        *execfakes.FakeTaskDelegate
		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory

		state exec.RunState
		repo  *build.Repository

		agentPlan atc.AgentPlan
		step      exec.Step
		runErr    error
		runOK     bool
	)

	containerMetadata := db.ContainerMetadata{
		WorkingDirectory: "some-artifact-root",
		Type:             db.ContainerTypeAgent,
		StepName:         "fixture-review",
		// The seal request carries this straight into the build occurrence,
		// whose Validate rejects an empty attempt (agent/snapshot/types.go).
		// The engine always sets it (atc/engine/builder.go); specs that seal
		// through a fake never notice, and this one does.
		Attempt: "1",
	}
	stepMetadata := exec.StepMetadata{
		TeamID: 123, BuildID: 1234, JobID: 12345, PipelineID: 555,
		TeamName: "main", SnapshotCreatedBy: "concourse", ExternalURL: "http://foo.bar",
	}
	planID := atc.PlanID("77")

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		fixtureCase = fixtureagent.CaseReviewAccept
		contentLimit = 0
		// nil means "no marker mount at all", which is what a plan with no
		// optional typed outputs produces.
		markerFiles = nil

		var found bool
		reviewSchema, found = contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
		Expect(found).To(BeTrue())

		inputDigest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
		Expect(err).NotTo(HaveOccurred())
		inputRef = snapshot.SnapshotRef{ID: 82, Type: snapshot.TypeRef("opaque/v1"), Digest: inputDigest}

		metadataStore = newFixtureMemoryMetadata()
		contentStore = newFixtureMemoryContent()
		locks = &fixtureLocks{}

		fakeStreamer = new(execfakes.FakeStreamer)
		fakeDelegate = new(execfakes.FakeTaskDelegate)
		// A production TaskDelegate always hands back writers, and the agent
		// step wraps stdout in the cost observer, which forwards to that writer
		// unconditionally (agent_cost_observer.go). An unstubbed fake therefore
		// panics on the first process write, before the seal boundary is
		// reached at all.
		fakeDelegate.StdoutReturns(gbytes.NewBuffer())
		fakeDelegate.StderrReturns(gbytes.NewBuffer())
		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)
		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		repo = state.ArtifactRepository()
		Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"change": {Artifact: runtimetest.NewVolume("fixture-input"), Snapshot: &inputRef},
		})).To(Succeed())

		agentPlan = atc.AgentPlan{
			Name: "fixture-review", Hermetic: true, Prompt: "unused by the fixture agent",
			Inputs:  []string{"change"},
			Outputs: []string{"review"},
			SnapshotInputs: map[string]atc.SnapshotInputConfig{
				"change": {Type: snapshot.TypeRef("opaque/v1")},
			},
			SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
				"review": {Type: snapshot.TypeRef("review/v1")},
			},
		}
	})

	AfterEach(func() { cancel() })

	JustBeforeEach(func() {
		raw, err := fixtureagent.Tar(fixtureCase, fixtureagent.Authority{
			RecordType:    "review/v1",
			RecordSchema:  reviewSchema.String(),
			SubjectInput:  "change",
			SubjectType:   inputRef.Type.String(),
			SubjectDigest: inputRef.Digest.String(),
			OversizeBytes: fixtureOversizeBytes,
		})
		Expect(err).NotTo(HaveOccurred())
		outputVolume = newFixtureTarVolume("fixture-review-output", raw)

		mounts := []runtime.VolumeMount{
			{Volume: outputVolume, MountPath: "some-artifact-root/review"},
			{Volume: runtimetest.NewVolume("flight-volume"), MountPath: "some-artifact-root/flight"},
		}
		if markerFiles != nil {
			// The optional-output control mount. It is deliberately separate
			// from every output mount so marker bytes never enter a snapshot,
			// and optionalOutputWasProduced streams it out GZIPPED — unlike the
			// output mount, which is raw — so runtimetest.Volume is the right
			// type here and fixtureTarVolume is not.
			mounts = append(mounts, runtime.VolumeMount{
				Volume:    runtimetest.NewVolume("typed-output-markers").WithContent(markerFiles),
				MountPath: "/tmp/.jetbridge/typed-output-markers/v1",
			})
		}

		owner := db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)
		worker := runtimetest.NewWorker("worker").WithContainer(
			owner,
			runtimetest.NewContainer().WithProcess(
				runtime.ProcessSpec{
					ID: "agent", Path: "agent-runner", Dir: "some-artifact-root",
					TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}},
				},
				runtimetest.ProcessStub{Attachable: true},
			),
			mounts,
		)
		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(worker, nil)

		registry, err := contracts.NewRegistry(
			contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}),
		)
		Expect(err).NotTo(HaveOccurred())
		canonicalizer := snapshot.Canonicalizer{TempDir: GinkgoT().TempDir(), MaxContentBytes: contentLimit}
		sealer, err := snapshot.NewBatchSealer(canonicalizer, registry, metadataStore, contentStore, locks)
		Expect(err).NotTo(HaveOccurred())

		step = exec.NewAgentStep(
			planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{},
			stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
			0, "registry.home/fixture-agent:e2e",
			exec.WithAgentOutputSealer(sealer),
			exec.WithAgentSnapshotStores(metadataStore, contentStore),
		)
		runOK, runErr = step.Run(ctx, state)
	})

	Context("when the fixture writes an accepted review/v1", func() {
		It("seals it through the real registry and publishes it to the build repository", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())

			entry, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot).NotTo(BeNil())
			Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
			Expect(entry.Snapshot.Digest.Validate()).To(Succeed())
			Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))

			// The bytes the sealer uploaded are the canonical archive keyed by
			// the digest the repository now advertises: content-addressing held
			// end to end, with no fake in between.
			body, err := contentStore.Open(ctx, snapshot.Snapshot{Digest: entry.Snapshot.Digest})
			Expect(err).NotTo(HaveOccurred())
			raw, err := io.ReadAll(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(body.Close()).To(Succeed())
			sum := sha256.Sum256(raw)
			Expect(snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))).To(Equal(entry.Snapshot.Digest))

			// The flight output stays legacy and untyped.
			flight, found := repo.ArtifactEntryFor("flight")
			Expect(found).To(BeTrue())
			Expect(flight.Snapshot).To(BeNil())
		})
	})

	Context("when the fixture writes a blocking changes-required review/v1", func() {
		BeforeEach(func() { fixtureCase = fixtureagent.CaseReviewChangesRequired })

		It("seals it too: a blocking finding is a valid judgment, not a malformed one", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())
			entry, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
		})
	})
})
