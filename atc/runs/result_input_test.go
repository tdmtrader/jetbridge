package runs_test

import (
	"context"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// resultInputSource answers the node's stat of a task input; nothing here
// selects a node.
type resultInputSource struct{ runs.ExecutionSource }

func (resultInputSource) ManagedInputTimeout(int) time.Duration { return time.Minute }

func (resultInputSource) StatTaskInput(context.Context, string, string, hangar.TreeRef) (output.PublishedObject, error) {
	return output.PublishedObject{}, nil
}

type resultInputMinter struct{}

func (resultInputMinter) Sign(output.ClaimRecord, output.ReadDestination, executioncontrol.NodeUID) (string, error) {
	return "warrant", nil
}

// A later task of the same Run job reads an earlier producer's result as a
// managed read of its published tree, bound from this build's own capture.
var _ = Describe("an in-Run result input", func() {
	var (
		ctx       context.Context
		build     db.Build
		author    atc.TaskPlan
		validate  atc.TaskPlan
		capture   db.RunCapture
		execution db.RunExecutionAdmission
		starter   *runs.ExecutionStarter
		spec      runtime.ContainerSpec
		captures  *db.HangarOutputRepository
		template  db.Pipeline
	)

	inTx := func(fn func(db.Tx) error) error {
		tx, err := dbConn.Begin()
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	}

	BeforeEach(func() {
		ctx = context.Background()
		authorStep := &atc.TaskStep{
			Name: "author", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "change", Output: "change"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "change"}}},
		}
		validateStep := &atc.TaskStep{
			Name: "validate", TaskID: uuid.NewString(),
			RunInputs: []atc.RunInput{{Name: "change", Input: "change", Result: "change"}},
			Config:    &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Inputs: []atc.TaskInputConfig{{Name: "change"}}},
		}
		template = savePipeline(defaultTeam, "result-inputs", atc.Config{Template: true, Jobs: atc.JobConfigs{
			{Name: "implement", PlanSequence: []atc.Step{{Config: authorStep}, {Config: validateStep}}},
		}})
		var creation db.RunCreation
		Expect(inTx(func(tx db.Tx) error {
			var err error
			creation, err = runFactory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{HangarOutput: true})
			return err
		})).To(Succeed())
		Expect(creation.EntryBuilds).To(HaveLen(1))
		build = creation.EntryBuilds[0]
		for _, step := range creation.Config.Jobs[0].PlanSequence {
			task := step.Config.(*atc.TaskStep)
			plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
			if task.Name == "author" {
				author = plan
			} else {
				validate = plan
			}
		}
		captures = db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
		starter = &runs.ExecutionStarter{Conn: dbConn, Factory: runFactory}
		spec = runtime.ContainerSpec{
			TeamID: defaultTeam.ID(), Type: db.ContainerTypeTask, Dir: "/tmp/build/validate",
			Inputs: []runtime.Input{{RunInput: "change", DestinationPath: "/tmp/build/validate/change"}},
		}
	})

	// start admits the author as the output starter does: a pending capture,
	// then the capturing execution under the capture's identity.
	start := func() {
		GinkgoHelper()
		Expect(inTx(func(tx db.Tx) error {
			var err error
			capture, err = runFactory.StartRunCapture(ctx, tx, build.ID(), author, time.Hour, "node", "node-uid")
			if err != nil {
				return err
			}
			execution, _, err = runFactory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
				BuildID: build.ID(), PlanID: "author", Kind: db.ContainerTypeTask,
				NodeName: "node", NodeUID: "node-uid", Capture: capture.Key(),
			})
			return err
		})).To(Succeed())
	}
	witness := func(kind executioncontrol.AcknowledgementKind, sequence executioncontrol.LedgerSequence, outcome *executioncontrol.ExitOutcome) {
		GinkgoHelper()
		Expect(inTx(func(tx db.Tx) error {
			return runFactory.RecordRunExecutionWitness(ctx, tx, build.ID(), "author", executioncontrol.Acknowledgement{
				ProtocolVersion: executioncontrol.ProtocolVersion, Kind: kind, Identity: execution.Identity,
				LedgerSequence: sequence, NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "author-process",
				ObservedAt: output.NewTimestamp(time.Now()), Outcome: outcome,
			})
		})).To(Succeed())
	}
	seal := func() {
		GinkgoHelper()
		start()
		witness(executioncontrol.AcknowledgementStart, 1, nil)
		witness(executioncontrol.AcknowledgementFinish, 2, &executioncontrol.ExitOutcome{ExitCode: 0})
		Expect(inTx(func(tx db.Tx) error {
			_, err := captures.CASPendingToPublishing(ctx, tx, capture.Key(), "pod-uid", "team-a", hangar.Digest("sha256:"+strings.Repeat("ab", 32)))
			return err
		})).To(Succeed())
	}
	publish := func() error {
		return inTx(func(tx db.Tx) error {
			_, err := captures.CASPublishingToPublished(ctx, tx, output.PublishedCapture{Key: capture.Key(), Generation: 1})
			return err
		})
	}
	fail := func() error {
		return inTx(func(tx db.Tx) error {
			_, err := captures.MarkFailed(ctx, tx, capture.Key(), "upload failed")
			return err
		})
	}
	load := func() db.RunTaskBinding {
		GinkgoHelper()
		var selected db.RunTaskBinding
		Expect(inTx(func(tx db.Tx) error {
			var err error
			selected, err = db.LoadRunTask(ctx, tx, build.ID(), validate.TaskID)
			return err
		})).To(Succeed())
		return selected
	}
	publishedRef := func() hangar.TreeRef {
		GinkgoHelper()
		var ref hangar.TreeRef
		Expect(inTx(func(tx db.Tx) error {
			c, err := captures.GetCapture(ctx, tx, capture.Key())
			if err == nil {
				ref, err = c.Ref()
			}
			return err
		})).To(Succeed())
		return ref
	}

	It("is never supplied at admission", func() {
		invocation := &db.RunInvocationIdentity{PrincipalDigest: strings.Repeat("a", 64), KeyDigest: strings.Repeat("b", 64)}
		err := inTx(func(tx db.Tx) error {
			_, err := runFactory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{
				HangarOutput: true, Invocation: invocation,
				Inputs: map[string]atc.RunInputSource{"change": {RunID: 1, Result: "change"}},
			})
			return err
		})
		Expect(err).To(MatchError(atc.ErrInvalidRunInputs))
	})

	Describe("LoadRunTask", func() {
		It("binds nothing while the build has no capture of the result", func() {
			selected := load()
			Expect(selected.Inputs).NotTo(HaveKey("change"))
			Expect(selected.Results).To(HaveKeyWithValue("change", output.CaptureState("")))
		})

		It("binds nothing while the capture is pending or publishing", func() {
			start()
			Expect(load().Results).To(HaveKeyWithValue("change", output.CapturePending))
			witness(executioncontrol.AcknowledgementStart, 1, nil)
			witness(executioncontrol.AcknowledgementFinish, 2, &executioncontrol.ExitOutcome{ExitCode: 0})
			Expect(inTx(func(tx db.Tx) error {
				_, err := captures.CASPendingToPublishing(ctx, tx, capture.Key(), "pod-uid", "team-a", hangar.Digest("sha256:"+strings.Repeat("ab", 32)))
				return err
			})).To(Succeed())
			selected := load()
			Expect(selected.Inputs).NotTo(HaveKey("change"))
			Expect(selected.Results).To(HaveKeyWithValue("change", output.CapturePublishing))
		})

		It("binds nothing once the capture is discarded", func() {
			start()
			Expect(inTx(func(tx db.Tx) error {
				_, err := captures.CASPendingToDiscarded(ctx, tx, capture.Key(), output.DiscardProducerFailed)
				return err
			})).To(Succeed())
			selected := load()
			Expect(selected.Inputs).NotTo(HaveKey("change"))
			Expect(selected.Results).To(HaveKeyWithValue("change", output.CaptureDiscarded))
		})

		It("binds the published tree under the capture's own publication claim", func() {
			seal()
			Expect(publish()).To(Succeed())
			selected := load()
			Expect(selected.Inputs).To(HaveKeyWithValue("change", atc.RunInputBinding{
				Source: atc.RunInputSource{RunID: selected.RunID, Result: "change"}, Ref: publishedRef(), ClaimID: capture.Key().ClaimID(),
			}))
		})
	})

	Describe("PrepareTask", func() {
		prepare := func(ctx context.Context) (runtime.ContainerSpec, error) {
			return starter.PrepareTask(ctx, build.ID(), validate, spec)
		}

		It("routes a published result to its input as a Hangar tree", func() {
			seal()
			Expect(publish()).To(Succeed())
			prepared, err := prepare(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(prepared.RunTaskID).To(Equal(validate.TaskID))
			Expect(prepared.Inputs[0].HangarTree).To(PointTo(Equal(publishedRef())))
		})

		It("waits for a publishing result to be published", func() {
			seal()
			published := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				time.Sleep(1500 * time.Millisecond)
				published <- publish()
			}()
			prepared, err := prepare(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(<-published).To(Succeed())
			Expect(prepared.Inputs[0].HangarTree).To(PointTo(Equal(publishedRef())))
		})

		It("stops waiting when the result fails to publish", func() {
			seal()
			failed := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				time.Sleep(1500 * time.Millisecond)
				failed <- fail()
			}()
			_, err := prepare(ctx)
			Expect(err).To(MatchError(atc.ErrRunInputUnavailable))
			Expect(err).To(MatchError(ContainSubstring("Run result change was not published")))
			Expect(<-failed).To(Succeed())
		})

		It("stops waiting when its context ends", func() {
			seal()
			bounded, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()
			_, err := prepare(bounded)
			Expect(err).To(MatchError(context.DeadlineExceeded))
		})

		It("does not wait for a result the build never captured", func() {
			_, err := prepare(ctx)
			Expect(err).To(MatchError(atc.ErrRunInputUnavailable))
		})

		It("does not wait for a discarded result", func() {
			start()
			Expect(inTx(func(tx db.Tx) error {
				_, err := captures.CASPendingToDiscarded(ctx, tx, capture.Key(), output.DiscardProducerFailed)
				return err
			})).To(Succeed())
			_, err := prepare(ctx)
			Expect(err).To(MatchError(atc.ErrRunInputUnavailable))
		})
	})

	It("is admitted as a managed read of the published tree", func() {
		seal()
		Expect(publish()).To(Succeed())
		prepared, err := starter.PrepareTask(ctx, build.ID(), validate, spec)
		Expect(err).NotTo(HaveOccurred())

		workers := db.NewWorkerFactory(dbConn, nil)
		worker, err := workers.SaveWorker(atc.Worker{Name: "result-input-worker", State: string(db.WorkerStateRunning), Platform: "linux"}, 0)
		Expect(err).NotTo(HaveOccurred())
		owner := db.NewBuildStepContainerOwner(build.ID(), "validate", defaultTeam.ID())
		container, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeTask})
		Expect(err).NotTo(HaveOccurred())
		var consumer db.RunExecutionAdmission
		Expect(inTx(func(tx db.Tx) error {
			consumer, _, err = runFactory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
				BuildID: build.ID(), PlanID: "validate", Kind: db.ContainerTypeTask, NodeName: "node", NodeUID: "node-uid",
			})
			return err
		})).To(Succeed())
		prepared.ExecutionControl = &runtime.ExecutionControl{Identity: consumer.Identity, Node: &runtime.ExecutionNode{Name: "node", UID: "node-uid"}}

		starter.Source = resultInputSource{}
		starter.SetInputReadMinter(resultInputMinter{})
		read, err := starter.PrepareInputs(ctx, owner, container.Handle(), []string{"change-volume"}, prepared)
		Expect(err).NotTo(HaveOccurred())
		Expect(read.Inputs[0].HangarRead).NotTo(BeNil())
		Expect(read.Inputs[0].HangarRead.Ref).To(Equal(publishedRef()))
		Expect(read.Inputs[0].HangarRead.Warrant).To(Equal("warrant"))

		var readers int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE consumer_binding_id = $1 AND released_at IS NULL`,
			"input-read:"+container.Handle()+"/change-volume").Scan(&readers)).To(Succeed())
		Expect(readers).To(Equal(1))
	})
})
