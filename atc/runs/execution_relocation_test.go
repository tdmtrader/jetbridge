package runs_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// relocationPlane is the node plane as RelocateContainer sees it: it records
// what it was asked and answers for whichever node it is asked about.
type relocationPlane struct {
	to, toUID string
	excluded  []string
	admitted  []string
}

func (p *relocationPlane) SelectNode(context.Context, runtime.ContainerSpec) (string, string, error) {
	return "node", "node-uid", nil
}

func (p *relocationPlane) SelectNodeExcept(_ context.Context, _ runtime.ContainerSpec, exclude []string) (string, string, error) {
	p.excluded = exclude
	return p.to, p.toUID, nil
}

func (p *relocationPlane) BaseRuntimeControl(_ context.Context, name, uid string, id executioncontrol.Identity) (*runtime.ExecutionControl, error) {
	p.admitted = append(p.admitted, name)
	return &runtime.ExecutionControl{
		Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted, Identity: id,
		Endpoint: "https://" + name, Capability: executioncontrol.ControlCapability("base-" + name),
		Node: &runtime.ExecutionNode{Name: name, UID: executioncontrol.NodeUID(uid)},
	}, nil
}

func (p *relocationPlane) RuntimeControl(ctx context.Context, c output.Capture) (*runtime.ExecutionControl, error) {
	control, err := p.BaseRuntimeControl(ctx, c.Node, string(c.NodeUID), c.Execution)
	if err != nil {
		return nil, err
	}
	control.Node = nil
	return control, control.SelectCapture(runtime.DurableOutputCapture{
		Version: runtime.DurableOutputCaptureVersion, Identity: c.Execution, Output: string(c.Key.Output),
		SourceControlGrant: executioncontrol.ControlCapability("hold-" + c.Node), CaptureDeadline: c.CaptureDeadline,
		Node: c.Node, NodeUID: c.NodeUID,
	})
}

var _ = Describe("Relocating a Run execution whose pod was never bound", func() {
	var (
		ctx     context.Context
		build   db.Build
		author  atc.TaskPlan
		plane   *relocationPlane
		starter *runs.ExecutionStarter
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
		template := savePipeline(defaultTeam, "relocations", atc.Config{Template: true, Jobs: atc.JobConfigs{
			{Name: "implement", PlanSequence: []atc.Step{{Config: authorStep}}},
		}})
		var creation db.RunCreation
		Expect(inTx(func(tx db.Tx) error {
			var err error
			creation, err = runFactory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{HangarOutput: true})
			return err
		})).To(Succeed())
		build = creation.EntryBuilds[0]
		task := creation.Config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
		author = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}
		plane = &relocationPlane{to: "roomy", toUID: "roomy-uid"}
		starter = &runs.ExecutionStarter{Conn: dbConn, Factory: runFactory, Source: plane}
	})

	owner := func(plan atc.PlanID) db.ContainerOwner {
		return db.NewBuildStepContainerOwner(build.ID(), plan, defaultTeam.ID())
	}

	admittedSpec := func(a db.RunExecutionAdmission) runtime.ContainerSpec {
		GinkgoHelper()
		control, err := plane.BaseRuntimeControl(ctx, a.NodeName, a.NodeUID, a.Identity)
		Expect(err).NotTo(HaveOccurred())
		return runtime.ContainerSpec{TeamID: defaultTeam.ID(), Type: db.ContainerTypeTask, Dir: "/tmp/build/author",
			Outputs: runtime.OutputPaths{"change": "/tmp/build/author/change"}, ExecutionControl: control}
	}

	admit := func(plan atc.PlanID) db.RunExecutionAdmission {
		GinkgoHelper()
		var a db.RunExecutionAdmission
		Expect(inTx(func(tx db.Tx) error {
			var err error
			a, _, err = runFactory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
				BuildID: build.ID(), PlanID: plan, Kind: db.ContainerTypeTask, NodeName: "full", NodeUID: "full-uid",
			})
			return err
		})).To(Succeed())
		return a
	}

	stored := func(plan atc.PlanID) db.RunExecutionAdmission {
		GinkgoHelper()
		var a db.RunExecutionAdmission
		Expect(inTx(func(tx db.Tx) error {
			var err error
			a, _, err = runFactory.RunExecution(ctx, tx, build.ID(), plan)
			return err
		})).To(Succeed())
		return a
	}

	It("admits the same execution on another node and pins the spec there", func() {
		a := admit("plain")
		spec, err := starter.RelocateContainer(ctx, owner("plain"), admittedSpec(a), []string{"tried-before"})
		Expect(err).NotTo(HaveOccurred())

		Expect(plane.excluded).To(ConsistOf("tried-before", "full"))
		Expect(spec.ExecutionControl.Identity).To(Equal(a.Identity))
		Expect(spec.ExecutionControl.Node.Name).To(Equal("roomy"))
		Expect(spec.ExecutionControl.Capability).To(BeEquivalentTo("base-roomy"))
		Expect(stored("plain").NodeName).To(Equal("roomy"))

		By("refusing the old spec's start check, which still names the full node")
		Expect(starter.CheckStart(ctx, owner("plain"), admittedSpec(a))).To(MatchError(output.ErrConflict))
		Expect(starter.CheckStart(ctx, owner("plain"), spec)).To(Succeed())
	})

	It("never moves an execution that started", func() {
		a := admit("plain")
		Expect(inTx(func(tx db.Tx) error {
			return runFactory.RecordRunExecutionWitness(ctx, tx, build.ID(), "plain", executioncontrol.Acknowledgement{
				ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
				Identity: a.Identity, LedgerSequence: 1, NodeUID: "full-uid", PodUID: "pod-uid",
				ProcessIdentity: "process", ObservedAt: output.NewTimestamp(time.Now()),
			})
		})).To(Succeed())

		_, err := starter.RelocateContainer(ctx, owner("plain"), admittedSpec(a), nil)
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(stored("plain").NodeName).To(Equal("full"))
		Expect(plane.admitted).NotTo(ContainElement("roomy"), "a refused move admitted the execution on the new node")
	})

	It("moves a capturing execution with its capture and a hold grant for the new node", func() {
		var a db.RunExecutionAdmission
		var capture db.RunCapture
		Expect(inTx(func(tx db.Tx) error {
			var err error
			capture, err = runFactory.StartRunCapture(ctx, tx, build.ID(), author, time.Hour, "full", "full-uid")
			if err != nil {
				return err
			}
			a, _, err = runFactory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
				BuildID: build.ID(), PlanID: "author", Kind: db.ContainerTypeTask,
				NodeName: "full", NodeUID: "full-uid", Capture: capture.Key(),
			})
			return err
		})).To(Succeed())
		held, err := plane.RuntimeControl(ctx, capture.Capture)
		Expect(err).NotTo(HaveOccurred())
		spec := admittedSpec(a)
		Expect(spec.ExecutionControl.SelectCapture(*held.Capture)).To(Succeed())
		Expect(spec.ExecutionControl.Validate(spec)).To(Succeed())

		moved, err := starter.RelocateContainer(ctx, owner("author"), spec, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(moved.ExecutionControl.Node.Name).To(Equal("roomy"))
		Expect(moved.ExecutionControl.Capture.Node).To(Equal("roomy"))
		Expect(moved.ExecutionControl.Capture.SourceControlGrant).To(BeEquivalentTo("hold-roomy"))
		Expect(moved.ExecutionControl.Capture.CaptureDeadline).To(Equal(spec.ExecutionControl.Capture.CaptureDeadline))

		var found db.RunCapture
		Expect(inTx(func(tx db.Tx) error {
			var err error
			found, _, err = runFactory.RunCaptureTask(ctx, tx, build.ID(), author.TaskID)
			return err
		})).To(Succeed())
		Expect(found.Capture.Node).To(Equal("roomy"))
		Expect(found.Capture.State).To(Equal(output.CapturePending))
	})

	It("refuses a container that is not a Run execution", func() {
		_, err := starter.RelocateContainer(ctx, owner("plain"), runtime.ContainerSpec{TeamID: defaultTeam.ID()}, nil)
		Expect(err).To(MatchError(runs.ErrRelocationUnavailable))
	})
})
