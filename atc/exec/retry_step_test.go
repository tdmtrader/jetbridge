package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func retrySnapshotRef(id int64) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID:     snapshot.SnapshotID(id),
		Type:   snapshot.TypeRef("repository/v1"),
		Digest: snapshot.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
}

func retryRunState() RunState {
	return NewRunState(func(atc.Plan) Step { return nil }, vars.StaticVariables{})
}

var _ = Describe("Retry Step", func() {
	var (
		ctx    context.Context
		cancel func()

		attempt1 *execfakes.FakeStep
		attempt2 *execfakes.FakeStep
		attempt3 *execfakes.FakeStep

		repo  *build.Repository
		state *execfakes.FakeRunState

		step Step
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		attempt1 = new(execfakes.FakeStep)
		attempt2 = new(execfakes.FakeStep)
		attempt3 = new(execfakes.FakeStep)

		repo = build.NewRepository()
		state = new(execfakes.FakeRunState)
		state.ArtifactRepositoryReturns(repo)
		state.NewArtifactScopeStub = func() RunState {
			attemptState := new(execfakes.FakeRunState)
			attemptState.ArtifactRepositoryReturns(repo.NewLocalScope())
			return attemptState
		}

		step = Retry(attempt1, attempt2, attempt3)
	})

	Describe("Run", func() {
		var stepOk bool
		var stepErr error

		JustBeforeEach(func() {
			stepOk, stepErr = step.Run(ctx, state)
		})

		Context("when attempt 1 succeeds", func() {
			BeforeEach(func() {
				attempt1.RunReturns(true, nil)
			})

			It("returns nil having only run the first attempt", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(0))
				Expect(attempt3.RunCallCount()).To(Equal(0))
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, and attempt 2 succeeds", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, nil)
				attempt2.RunReturns(true, nil)
			})

			It("returns nil having only run the first and second attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(0))
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 errors, and attempt 2 succeeds", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, errors.New("nope"))
				attempt2.RunReturns(true, nil)
			})

			It("returns nil having only run the first and second attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(0))
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 errors, and attempt 2 is interrupted", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, errors.New("nope"))
				attempt2.RunStub = func(c context.Context, r RunState) (bool, error) {
					cancel()
					return false, c.Err()
				}
			})

			It("returns the context error having only run the first and second attempts", func() {
				Expect(stepErr).To(Equal(context.Canceled))

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(0))
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when attempt 1 errors, attempt 2 times out, and attempt 3 succeeds", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, errors.New("nope"))
				attempt2.RunStub = func(c context.Context, r RunState) (bool, error) {
					timeout, subCancel := context.WithTimeout(c, 0)
					defer subCancel()
					<-timeout.Done()
					return false, timeout.Err()
				}
				attempt3.RunReturns(true, nil)
			})

			It("returns nil after running all 3 steps", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(1))
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 succeeds", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, nil)
				attempt2.RunReturns(false, nil)
				attempt3.RunReturns(true, nil)
			})

			It("returns nil after running all 3 steps", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(1))
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 errors", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				attempt1.RunReturns(false, nil)
				attempt2.RunReturns(false, nil)
				attempt3.RunReturns(false, disaster)
			})

			It("returns the error", func() {
				Expect(stepErr).To(Equal(disaster))

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(1))
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 fails", func() {
			BeforeEach(func() {
				attempt1.RunReturns(false, nil)
				attempt2.RunReturns(false, nil)
				attempt3.RunReturns(false, nil)
			})

			It("returns nil having only run the first and second attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attempt1.RunCallCount()).To(Equal(1))
				Expect(attempt2.RunCallCount()).To(Equal(1))
				Expect(attempt3.RunCallCount()).To(Equal(1))
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("with attempt-scoped artifact publication", func() {
			It("discards a false attempt and publishes only the successful attempt", func() {
				state := retryRunState()
				firstRef := retrySnapshotRef(1)
				secondRef := retrySnapshotRef(2)
				attempt1.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					Expect(attemptState.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"output": {Artifact: runtimetest.NewVolume("first"), Snapshot: &firstRef},
					})).To(Succeed())
					attemptState.ArtifactRepository().RegisterImageRef("output", "docker:///first@sha256:111")
					return false, nil
				}
				attempt2.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					_, _, found := attemptState.ArtifactRepository().ArtifactFor("output")
					Expect(found).To(BeFalse())
					_, found = attemptState.ArtifactRepository().SnapshotFor("output")
					Expect(found).To(BeFalse())
					_, found = attemptState.ArtifactRepository().ImageRefFor("output")
					Expect(found).To(BeFalse())
					Expect(attemptState.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"output": {Artifact: runtimetest.NewVolume("second"), Snapshot: &secondRef},
					})).To(Succeed())
					return true, nil
				}

				ok, err := Retry(attempt1, attempt2).Run(ctx, state)

				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				entry, found := state.ArtifactRepository().ArtifactEntryFor("output")
				Expect(found).To(BeTrue())
				Expect(entry.Artifact.Handle()).To(Equal("second"))
				Expect(entry.Snapshot).To(Equal(&secondRef))
			})

			It("discards an errored attempt before the next attempt", func() {
				state := retryRunState()
				firstRef := retrySnapshotRef(1)
				attempt1.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					Expect(attemptState.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"output": {Artifact: runtimetest.NewVolume("first"), Snapshot: &firstRef},
					})).To(Succeed())
					return false, errors.New("transient")
				}
				attempt2.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					_, _, found := attemptState.ArtifactRepository().ArtifactFor("output")
					Expect(found).To(BeFalse())
					return true, nil
				}

				ok, err := Retry(attempt1, attempt2).Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(state.ArtifactRepository().AsMap()).To(BeEmpty())
			})

			It("discards a canceled successful-looking attempt", func() {
				state := retryRunState()
				ref := retrySnapshotRef(1)
				secondRan := false
				attempt1.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					Expect(attemptState.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"output": {Artifact: runtimetest.NewVolume("candidate"), Snapshot: &ref},
					})).To(Succeed())
					cancel()
					return true, nil
				}
				attempt2.RunStub = func(context.Context, RunState) (bool, error) {
					secondRan = true
					return true, nil
				}

				ok, err := Retry(attempt1, attempt2).Run(ctx, state)
				Expect(err).To(Equal(context.Canceled))
				Expect(ok).To(BeFalse())
				Expect(secondRan).To(BeFalse())
				Expect(state.ArtifactRepository().AsMap()).To(BeEmpty())
			})

			It("terminates on a commit conflict without publishing part of the attempt", func() {
				state := retryRunState()
				parent := state.ArtifactRepository()
				secondRan := false
				base := retrySnapshotRef(1)
				Expect(parent.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"contended": {Artifact: runtimetest.NewVolume("base"), Snapshot: &base},
				})).To(Succeed())
				attempt1.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					candidate := retrySnapshotRef(2)
					Expect(attemptState.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"contended": {Artifact: runtimetest.NewVolume("candidate"), Snapshot: &candidate},
						"other":     {Artifact: runtimetest.NewVolume("must-not-leak")},
					})).To(Succeed())
					raced := retrySnapshotRef(3)
					Expect(parent.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"contended": {Artifact: runtimetest.NewVolume("raced"), Snapshot: &raced},
					})).To(Succeed())
					return true, nil
				}
				attempt2.RunStub = func(context.Context, RunState) (bool, error) {
					secondRan = true
					return true, nil
				}

				ok, err := Retry(attempt1, attempt2).Run(ctx, state)
				Expect(err).To(MatchError(ContainSubstring("contended")))
				Expect(ok).To(BeFalse())
				Expect(secondRan).To(BeFalse())
				entry, found := parent.ArtifactEntryFor("contended")
				Expect(found).To(BeTrue())
				Expect(entry.Artifact.Handle()).To(Equal("raced"))
				Expect(parent.AsMap()).NotTo(HaveKey(build.ArtifactName("other")))
			})

			It("uses a fresh artifact repository per attempt while sharing variables and results", func() {
				state := retryRunState()
				var attemptRepositories []*build.Repository
				attempt1.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					attemptRepositories = append(attemptRepositories, attemptState.ArtifactRepository())
					attemptState.AddLocalVar("get-metadata", "preserved", false)
					attemptState.StoreResult("get-result", "preserved-result")
					return false, nil
				}
				attempt2.RunStub = func(_ context.Context, attemptState RunState) (bool, error) {
					attemptRepositories = append(attemptRepositories, attemptState.ArtifactRepository())
					value, found, err := attemptState.Get(vars.Reference{Source: ".", Path: "get-metadata"})
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(value).To(Equal("preserved"))
					var result string
					Expect(attemptState.Result("get-result", &result)).To(BeTrue())
					Expect(result).To(Equal("preserved-result"))
					return true, nil
				}

				ok, err := Retry(attempt1, attempt2).Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(attemptRepositories).To(HaveLen(2))
				Expect(attemptRepositories[0]).NotTo(BeIdenticalTo(attemptRepositories[1]))
				Expect(attemptRepositories[0]).NotTo(BeIdenticalTo(state.ArtifactRepository()))
				Expect(attemptRepositories[1]).NotTo(BeIdenticalTo(state.ArtifactRepository()))
			})
		})

		It("preserves the zero-attempt result", func() {
			ok, err := Retry().Run(ctx, retryRunState())

			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
	})
})
