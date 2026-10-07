package atctest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// Producer is one Run's result producer as the runtime runs it on the output
// node: its output source reserved and held by its Pod, and its execution
// admitted and witnessed started. From then on the Run's credential session
// for the result is available, exactly as when a real Pod has started.
type Producer struct {
	p       *Platform
	RunID   int
	result  string
	buildID int
	plan    atc.TaskPlan
	planID  atc.PlanID
	record  output.HandoffRecord

	mu        sync.Mutex
	published bool
}

type producerKey struct {
	run    int
	result string
}

// Start starts the producer of result in Run number of template, or returns
// the one already started.
func (p *Platform) Start(t testing.TB, team, template string, number int, result string) *Producer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	producer, err := p.start(ctx, team, template, number, result)
	if err != nil {
		t.Fatalf("starting the %s producer of %s/%s Run %d: %v", result, team, template, number, err)
	}
	return producer
}

// Schedule starts the result producer of every Run of template as it is
// admitted, until the test ends: the scheduler and the kubelet, for the one
// step whose start the credential handoff waits for.
func (p *Platform) Schedule(t testing.TB, team, template, result string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			var numbers []int
			rows, err := p.conn.QueryContext(ctx, `SELECT r.number FROM pipeline_runs r JOIN pipelines p ON p.id=r.template_pipeline_id
 JOIN teams t ON t.id=p.team_id WHERE t.name=$1 AND p.name=$2 AND r.status='running'`, team, template)
			if err == nil {
				for rows.Next() {
					var number int
					if rows.Scan(&number) == nil {
						numbers = append(numbers, number)
					}
				}
				rows.Close()
			}
			for _, number := range numbers {
				if _, err := p.start(ctx, team, template, number, result); err != nil && ctx.Err() == nil {
					t.Errorf("scheduling the %s producer of %s/%s Run %d: %v", result, team, template, number, err)
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
}

func (p *Platform) start(ctx context.Context, team, template string, number int, result string) (*Producer, error) {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	run, definition, err := p.run(team, template, number)
	if err != nil {
		return nil, err
	}
	if started, ok := p.producers.Load(producerKey{run.ID(), result}); ok {
		return started.(*Producer), nil
	}
	producer := &Producer{p: p, RunID: run.ID(), result: result}
	var job string
	for _, config := range definition.Materialized.Jobs {
		_ = config.StepConfig().Visit(atc.StepRecursor{OnTask: func(task *atc.TaskStep) error {
			if task.RunResult != nil && task.RunResult.Name == result {
				job = config.Name
				producer.plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
			}
			return nil
		}})
	}
	if job == "" {
		return nil, fmt.Errorf("the Run declares no %s result", result)
	}
	if err := p.conn.QueryRowContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, run.ID(), job).Scan(&producer.buildID); err != nil {
		return nil, err
	}
	producer.planID = atc.PlanID("task-" + producer.plan.TaskID)
	n := p.node
	outputs := p.outputs()

	// The output starter's admission: predeclare the exact execution, reserve
	// its source on the node, record the reservation.
	if err := p.inTx(ctx, func(tx db.Tx) (err error) {
		producer.record, err = p.runs.PredeclareOutputTask(ctx, tx, producer.buildID, producer.plan, Epoch, time.Hour, nodeName, string(n.uid))
		if err != nil {
			return err
		}
		return p.runs.RequestOutputSource(ctx, tx, producer.buildID, producer.plan, Epoch)
	}); err != nil {
		return nil, err
	}
	r := producer.record
	observe, err := n.client.MintGrant(executioncontrol.BaseFacet, "observe", r.Execution)
	if err != nil {
		return nil, err
	}
	if _, err := n.client.Admit(ctx, executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: r.Execution,
		ActivationEpoch: r.ActivationEpoch, NodeUID: n.uid, Capability: observe}); err != nil {
		return nil, err
	}
	reserved, err := n.client.ReserveIncarnation(ctx, output.CaptureAdmission{ProtocolVersion: output.ProtocolVersion, Execution: r.Execution,
		ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline})
	if err != nil {
		return nil, err
	}
	if err := p.inTx(ctx, func(tx db.Tx) (err error) {
		if err = p.runs.RecordOutputSource(ctx, tx, producer.buildID, producer.plan, reserved, nodeName); err != nil {
			return err
		}
		producer.record, err = p.runs.PredeclareOutputTask(ctx, tx, producer.buildID, producer.plan, Epoch, time.Hour, nodeName, string(n.uid))
		return err
	}); err != nil {
		return nil, err
	}

	// The Pod: its capture init holds the source, its process starts.
	pod := executioncontrol.PodUID(uuid.NewString())
	if err := n.hold(ctx, producer.record, pod); err != nil {
		return nil, err
	}
	hold, err := n.client.InspectHold(ctx, r.Execution, r.HandoffID)
	if err != nil {
		return nil, err
	}
	if err := p.inTx(ctx, func(tx db.Tx) error { return outputs.AcknowledgeSourceHold(ctx, tx, hold) }); err != nil {
		return nil, err
	}
	started, err := n.client.RecordStart(ctx, r.Execution, hold.PodUID, executioncontrol.ProcessIdentity(uuid.NewString()))
	if err != nil {
		return nil, err
	}
	if err := p.inTx(ctx, func(tx db.Tx) error {
		admitted, _, err := p.runs.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{BuildID: producer.buildID, PlanID: producer.planID,
			Kind: db.ContainerTypeTask, Epoch: Epoch, NodeName: nodeName, NodeUID: string(n.uid), HandoffID: r.HandoffID})
		if err != nil {
			return err
		}
		if admitted.Identity != started.Identity {
			return errors.New("the node started a different execution")
		}
		return p.runs.RecordRunExecutionWitness(ctx, tx, producer.buildID, producer.planID, started, n.control)
	}); err != nil {
		return nil, err
	}
	p.producers.Store(producerKey{run.ID(), result}, producer)
	return producer, nil
}

// Publish writes files into the producer's reserved output as its task
// would, lets its process exit 0, and takes the capture through seal,
// publication, receipt and release.
func (producer *Producer) Publish(t testing.TB, files map[string][]byte) {
	t.Helper()
	if err := producer.publish(files); err != nil {
		t.Fatalf("publishing the %s result of Run %d: %v", producer.result, producer.RunID, err)
	}
}

func (producer *Producer) publish(files map[string][]byte) error {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	if producer.published {
		return errors.New("already published")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, n, r := producer.p, producer.p.node, producer.record
	directory := filepath.Join(n.steps, r.Source.Directory)
	for name, data := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(directory, name)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o644); err != nil {
			return err
		}
	}
	if _, err := n.client.RecordOutcome(ctx, r.Execution, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		return err
	}
	outputs := p.outputs()
	coordinator := &hangaroutput.Coordinator{
		Transactor: transactor{p.conn}, Repository: outputs,
		Dialer: hangaroutput.SourceDialerFunc(func(string) (hangaroutput.SourceControl, error) { return n.client, nil }),
		Drain:  n, Verifier: outputs.receipts, HoldVerifier: n.control,
		Announcer: hangaroutput.AnnouncerFunc(outputs.RecordAnnouncement), OwnerID: uuid.NewString(), ReceiptKeyID: receiptKeyID,
	}
	registered := false
	for range 20 {
		if _, err := coordinator.Advance(ctx, r.HandoffID); err != nil {
			return err
		}
		var current db.RunOutputTask
		if err := p.inTx(ctx, func(tx db.Tx) (err error) {
			current, _, err = p.runs.OutputTask(ctx, tx, producer.buildID, producer.plan.TaskID)
			return err
		}); err != nil {
			return err
		}
		if current.Record.State == output.CaptureStateRegistered && current.Record.Receipt != nil {
			registered = true
			break
		}
	}
	if !registered {
		return errors.New("the capture never reached a verified receipt")
	}
	finished, err := n.client.Classify(ctx, r.Execution)
	if err != nil || finished.Acknowledgement == nil {
		return fmt.Errorf("the node has no finish: %v", err)
	}
	if err := p.inTx(ctx, func(tx db.Tx) error {
		return p.runs.RecordRunExecutionWitness(ctx, tx, producer.buildID, producer.planID, *finished.Acknowledgement, n.control)
	}); err != nil {
		return err
	}
	var record output.HandoffRecord
	if err := p.inTx(ctx, func(tx db.Tx) (err error) {
		record, err = outputs.LoadHandoffRecord(ctx, tx, r.HandoffID)
		return err
	}); err != nil {
		return err
	}
	if record.Disposition == nil {
		return errors.New("the capture has no disposition to release")
	}
	released, err := n.client.AcknowledgeRelease(ctx, output.ReleaseIntent{ProtocolVersion: output.ProtocolVersion, Disposition: *record.Disposition,
		Execution: record.Execution, ActivationEpoch: record.ActivationEpoch, HandoffID: record.HandoffID, SourceHoldID: record.SourceHoldID,
		ReleaseIntentID: record.ReleaseIntentID, Incarnation: record.Source.Incarnation})
	if err != nil {
		return err
	}
	if err := p.inTx(ctx, func(tx db.Tx) error { return outputs.AcknowledgeCaptureRelease(ctx, tx, released) }); err != nil {
		return err
	}
	producer.published = true
	return nil
}

// Succeed publishes each named result of Run number -- starting any producer
// not yet started -- then finishes the Run's builds and finalizes it, so the
// Run is succeeded with those results bound.
func (p *Platform) Succeed(t testing.TB, team, template string, number int, results map[string]map[string][]byte) {
	t.Helper()
	for result, files := range results {
		p.Start(t, team, template, number, result).Publish(t, files)
	}
	p.finish(t, team, template, number, db.BuildStatusSucceeded)
}

// Fail finishes Run number's builds failed and finalizes it.
func (p *Platform) Fail(t testing.TB, team, template string, number int) {
	t.Helper()
	p.finish(t, team, template, number, db.BuildStatusFailed)
}

func (p *Platform) finish(t testing.TB, team, template string, number int, status db.BuildStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run, definition, err := p.run(team, template, number)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.conn.QueryContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id=$1`, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		build, found, err := p.builds.Build(id)
		if err != nil || !found {
			t.Fatalf("build %d: %v", id, err)
		}
		if err := build.Finish(status); err != nil {
			t.Fatal(err)
		}
	}
	payload, found, err := p.runs.InstancePipeline(run)
	if err != nil || !found {
		t.Fatalf("the Run's payload: %v", err)
	}
	for _, config := range definition.Materialized.Jobs {
		job, found, err := payload.Job(config.Name)
		if err != nil || !found {
			t.Fatalf("job %s: %v", config.Name, err)
		}
		if err := job.ConsumeScheduleRequest(time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	var completed bool
	if err := p.inTx(ctx, func(tx db.Tx) (err error) {
		completed, err = p.runs.FinalizeOutputRun(ctx, tx, run.ID())
		return err
	}); err != nil || !completed {
		t.Fatalf("finalizing Run %d: completed=%v %v", run.ID(), completed, err)
	}
}

// Delivered is what the producers of a Run received through credential
// handoff, and how many deliveries reached the node.
func (p *Platform) Delivered(runID int) ([]byte, int) {
	p.node.mu.Lock()
	defer p.node.mu.Unlock()
	return p.node.delivered[runID], p.node.attempts[runID]
}

// RefuseDelivery makes every credential delivery into a producer of team's
// Runs fail at the node after the stream was sent: a severed exec. The
// delivery counts as an attempt and delivers nothing.
func (p *Platform) RefuseDelivery(team string) {
	p.refusedMu.Lock()
	defer p.refusedMu.Unlock()
	p.refusedTeams[team] = true
}

func (p *Platform) deliveryRefused(runID int) bool {
	var team string
	if err := p.conn.QueryRow(`SELECT t.name FROM pipeline_runs r JOIN pipelines p ON p.id=r.template_pipeline_id
 JOIN teams t ON t.id=p.team_id WHERE r.id=$1`, runID).Scan(&team); err != nil {
		return true
	}
	p.refusedMu.Lock()
	defer p.refusedMu.Unlock()
	return p.refusedTeams[team]
}

// Input materializes the sealed input a Run bound under name through the
// node's managed read, and returns the directory it was written to, which
// also holds the node's .hangar-materialized marker.
func (p *Platform) Input(t testing.TB, team, template string, number int, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run, definition, err := p.run(team, template, number)
	if err != nil {
		t.Fatal(err)
	}
	var job, taskID string
	for _, config := range definition.Materialized.Jobs {
		_ = config.StepConfig().Visit(atc.StepRecursor{OnTask: func(task *atc.TaskStep) error {
			for _, input := range task.RunInputs {
				if input.Name == name && taskID == "" {
					job, taskID = config.Name, task.TaskID
				}
			}
			return nil
		}})
	}
	var buildID int
	if err := p.conn.QueryRowContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, run.ID(), job).Scan(&buildID); err != nil {
		t.Fatalf("the Run takes no input %s: %v", name, err)
	}
	var binding atc.RunInputBinding
	if err := p.inTx(ctx, func(tx db.Tx) error {
		task, err := db.LoadRunTask(ctx, tx, buildID, taskID, Epoch)
		binding = task.Inputs[name]
		return err
	}); err != nil {
		t.Fatal(err)
	}
	nonce, err := output.NewReadWarrantNonce(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := db.HangarConsumerPrefixHeld("atctest-input")
	if err != nil {
		t.Fatal(err)
	}
	destination := output.ReadDestination{Handle: uuid.NewString(), Volume: "input"}
	admission := hangaroutput.ReadAdmission{Transactor: transactor{p.conn}, Leases: db.NewHangarOutputRepository(prefix), Stat: p.node.client,
		Minter: p.node.warrants, Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() })}
	warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{ReadLeaseID: output.ReadLeaseID(uuid.NewString()), WarrantNonce: nonce,
		ClaimID: binding.ClaimID, Ref: binding.Ref, Destination: destination, ActivationEpoch: Epoch, MaterializationTimeout: time.Minute, NodeUID: p.node.uid})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.node.client.MaterializeManagedOutput(ctx, output.ManagedReadRequest{Ref: binding.Ref, Destination: destination, Warrant: warrant.Token}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(p.node.steps, destination.Handle, destination.Volume)
}

func (p *Platform) run(team, template string, number int) (db.PipelineRun, atc.RunDefinition, error) {
	found, ok, err := p.teams.FindTeam(team)
	if err != nil || !ok {
		return nil, atc.RunDefinition{}, fmt.Errorf("team %s: %v", team, err)
	}
	pipeline, ok, err := found.Pipeline(atc.PipelineRef{Name: template})
	if err != nil || !ok {
		return nil, atc.RunDefinition{}, fmt.Errorf("template %s: %v", template, err)
	}
	run, ok, err := p.runs.GetRun(pipeline, number)
	if err != nil || !ok {
		return nil, atc.RunDefinition{}, fmt.Errorf("Run %d: %v", number, err)
	}
	definition, ok, err := p.runs.Definition(run.ID())
	if err != nil || !ok {
		return nil, atc.RunDefinition{}, fmt.Errorf("Run %d has no definition: %v", number, err)
	}
	return run, definition, nil
}

type runOutputs struct {
	*db.RunOutputRepository
	receipts *output.ReceiptSignatureVerifier
}

func (p *Platform) outputs() runOutputs {
	verifier, err := p.node.receipt.SignatureVerifier(output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		panic(err)
	}
	return runOutputs{db.NewRunOutputRepository(db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()), p.node.control, verifier), verifier}
}

func (p *Platform) inTx(ctx context.Context, work func(db.Tx) error) error {
	tx, err := p.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := work(tx); err != nil {
		return err
	}
	return tx.Commit()
}
