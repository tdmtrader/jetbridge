package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// sealedCaptures answers one Run build's ownership and its producer's capture
// state; every other question goes to the real factory.
type sealedCaptures struct {
	db.PipelineRunFactory
	state output.CaptureState
	found bool
}

func (f *sealedCaptures) RunExecutionOwner(context.Context, db.Tx, int) (int, bool, error) {
	return 1, true, nil
}

func (f *sealedCaptures) RunCaptureTask(_ context.Context, _ db.Tx, buildID int, taskID string) (db.RunCapture, bool, error) {
	return db.RunCapture{BuildID: buildID, TaskID: taskID, Capture: output.Capture{State: f.state}}, f.found, nil
}

// A re-driven build finds a sealed producer's container only to read its
// outputs. The spec's claim that the producer is sealed is checked against
// the capture row: a claim the row does not back admits nothing.
var _ = Describe("a sealed Run task's container", func() {
	var (
		captures *sealedCaptures
		starter  *runs.ExecutionStarter
		owner    db.ContainerOwner
		spec     runtime.ContainerSpec
	)

	BeforeEach(func() {
		captures = &sealedCaptures{PipelineRunFactory: db.NewPipelineRunFactory(dbConn, lockFactory)}
		// No node source: anything past the sealed check would be refused.
		starter = &runs.ExecutionStarter{Conn: dbConn, Factory: captures}
		owner = db.NewBuildStepContainerOwner(7, "plan", 1)
		spec = runtime.ContainerSpec{
			TeamID: 1, Type: db.ContainerTypeTask,
			RunTaskID: "6f1c3b8e-2f5d-4f7a-9b8e-0d2c4a6e8f10", RunTaskSealed: true,
		}
	})

	prepare := func() error {
		_, err := starter.PrepareContainer(context.Background(), owner, db.ContainerMetadata{Type: db.ContainerTypeTask}, spec)
		if err != nil {
			return err
		}
		_, err = starter.PrepareInputs(context.Background(), owner, "handle", nil, spec)
		return err
	}

	for _, state := range []output.CaptureState{output.CapturePublishing, output.CapturePublished} {
		It("is found without admission once the capture is "+string(state), func() {
			captures.state, captures.found = state, true
			Expect(prepare()).To(Succeed())
		})
	}

	for _, state := range []output.CaptureState{output.CapturePending, output.CaptureDiscarded, output.CaptureFailed} {
		It("is refused while the capture is "+string(state), func() {
			captures.state, captures.found = state, true
			Expect(prepare()).To(MatchError(atc.ErrRunResultsUnavailable))
		})
	}

	It("is refused when the task has no capture", func() {
		Expect(prepare()).To(MatchError(atc.ErrRunResultsUnavailable))
	})
})
