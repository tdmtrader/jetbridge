package steps

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// runCaptureRecord is a Run producer's capture as the Run reads it back: the
// capture row, and its tree ref once it is published.
type runCaptureRecord struct {
	output.Capture
	Ref hangar.TreeRef
}

func runCaptureRecordOf(capture output.Capture) runCaptureRecord {
	record := runCaptureRecord{Capture: capture}
	if ref, err := capture.Ref(); err == nil {
		record.Ref = ref
	}

	return record
}

type RunOutputStart struct {
	DB             JetbridgeDB
	Creation       db.RunCreation
	Plan           atc.TaskPlan
	Daemon         HangarDaemon
	Record, Replay runCaptureRecord
	Err            error
}

func RunOutputStartDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, RunOutputStart]("two controllers concurrently admit the producer", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.DB.Conn.SetMaxOpenConns(4)
			var records [2]runCaptureRecord
			var errs [2]error
			var group sync.WaitGroup
			start := make(chan struct{})
			for i := range records {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					<-start
					records[i], errs[i] = in.start(in.Plan, brineCaptureNode, hangarNodeUID, false)
				}(i)
			}
			close(start)
			group.Wait()
			for _, err := range errs {
				if err != nil {
					return in, err
				}
			}
			in.Record, in.Replay = records[0], records[1]
			return in, nil
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its build tries to finish before its capture settles", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Err = in.Creation.EntryBuilds[0].Finish(db.BuildStatusSucceeded)
			return in, nil
		}),
		CheckThat[RunOutputStart]("the build remains unfinished and the capture is retained", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("the producer became externally terminal before its capture settled")
			}
			var completed bool
			if err := in.DB.Conn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, in.Creation.EntryBuilds[0].ID()).Scan(&completed); err != nil {
				return err
			}
			if completed {
				return fmt.Errorf("refused completion still changed the build")
			}
			in.Err = nil
			return checkRunOutputStart(in)
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("deletion of its capture link is attempted", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			_, in.Err = in.DB.Conn.Exec(`DELETE FROM pipeline_run_captures WHERE run_id=$1`, in.Creation.Run.ID())
			return in, nil
		}),
		CheckThat[RunOutputStart]("the original capture link remains immutable", func(in RunOutputStart) error {
			// Only the team's purge may delete Run evidence; anything else is
			// refused by the link's own trigger.
			if in.Err == nil || !strings.Contains(in.Err.Error(), "Run evidence is deleted only by its team's purge") {
				return fmt.Errorf("the capture link was removable: %v", in.Err)
			}
			in.Err = nil
			return checkRunOutputStart(in)
		}),
		CheckThat[RetainedRunDefinition]("the Run records an immutable v2 birth contract", func(in RetainedRunDefinition) error {
			var version string
			var epoch *int64
			if err := in.DB.Conn.QueryRow(`SELECT run_contract_version, activation_epoch FROM pipeline_runs WHERE id=$1`, in.Creation.Run.ID()).Scan(&version, &epoch); err != nil {
				return err
			}
			if version != "v2" || epoch == nil || *epoch <= 0 {
				return fmt.Errorf("birth was misclassified: %q %v", version, epoch)
			}
			_, err := in.DB.Conn.Exec(`UPDATE pipeline_runs SET activation_epoch=activation_epoch+1 WHERE id=$1`, in.Creation.Run.ID())
			if err == nil || !strings.Contains(err.Error(), "immutable") {
				return fmt.Errorf("birth contract was mutable: %v", err)
			}
			if _, err = in.DB.Conn.Exec(`UPDATE pipeline_runs SET run_contract_version='legacy_v1' WHERE id=$1`, in.Creation.Run.ID()); err == nil {
				return fmt.Errorf("a Run was reclassified out of the one contract class")
			}
			return nil
		}),
		brine.DefineMapUsing[brine.Empty, RunOutputStart]("an internally admitted v2 result Run", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputStart, error) {
			in, err := runOutputFixture(rec, res, "current")
			if err == nil {
				err = in.Err
			}
			return in, err
		}),
		brine.DefineMapUsing[brine.Empty, RunOutputStart]("internal v2 admission with {string} activation", []string{"jetbridge-db"}, func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputStart, error) {
			activation, _ := p.GetString(0)
			return runOutputFixture(rec, res, activation)
		}),
		CheckThat[RunOutputStart]("v2 admission is refused before allocating a Run", func(in RunOutputStart) error {
			if in.Err == nil || !strings.Contains(in.Err.Error(), "not activated") {
				return fmt.Errorf("expected activation refusal, got %v", in.Err)
			}
			var count, number int
			if err := in.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_runs), coalesce(max(last_run_number),0) FROM pipelines`).Scan(&count, &number); err != nil {
				return err
			}
			if count != 0 || number != 0 {
				return fmt.Errorf("unavailable v2 admission allocated %d Runs and number %d", count, number)
			}
			return nil
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its result producer requests start admission", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Record, in.Err = in.start(in.Plan, brineCaptureNode, hangarNodeUID, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("a new controller repeats the producer admission", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Replay, in.Err = in.start(in.Plan, brineCaptureNode, hangarNodeUID, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its producer admission transaction is rolled back", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Record, in.Err = in.start(in.Plan, brineCaptureNode, hangarNodeUID, true)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its producer asks to start with a different {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			fact, _ := p.GetString(0)
			plan := in.Plan
			selected := *plan.RunResult
			plan.RunResult = &selected
			var err error
			switch fact {
			case "task":
				plan.TaskID = freshUUID()
			case "result":
				plan.RunResult.Name = "other"
			case "output":
				plan.RunResult.Output = "other"
			case "job":
				in.Creation.EntryBuilds = []db.Build{in.Creation.EntryBuilds[1]}
			case "completed Run":
				for _, build := range in.Creation.EntryBuilds {
					if err = build.Finish(db.BuildStatusFailed); err != nil {
						break
					}
				}
				if err == nil {
					err = consumeRunScheduling(in)
				}
				if err == nil {
					closed := finalizeRunResult(RunResultPublication{Start: in}, false)
					err = closed.Err
					if err == nil && !closed.Completed {
						err = fmt.Errorf("fixture Run did not finish")
					}
				}
			case "aborted build":
				_, err = in.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, in.Creation.EntryBuilds[0].ID())
			default:
				return in, fmt.Errorf("unknown mutation %s", fact)
			}
			if err != nil {
				return in, err
			}
			_, in.Err = in.start(plan, "brine-node", hangarNodeUID, false)
			return in, nil
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("another node is offered for that producer", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			_, in.Err = in.start(in.Plan, "replacement-node", "replacement-uid", false)
			return in, nil
		}),
		CheckThat[RunOutputStart]("one pending capture belongs to that exact Run and build", checkRunOutputStart),
		CheckThat[RunOutputStart]("neither a capture link nor a capture row remains", checkNoRunOutputStart),
		CheckThat[RunOutputStart]("start admission is refused without a capture", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("unadmitted producer was allowed to start")
			}
			return checkNoRunOutputStart(in)
		}),
		CheckThat[RunOutputStart]("the original node and capture remain authoritative", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("another node replaced the admitted producer")
			}
			in.Err = nil
			return checkRunOutputStart(in)
		}),
	}
}

func runOutputFixture(rec *brine.Recorder, res brine.Resources, activation string) (RunOutputStart, error) {
	return runOutputFixtureOnNode(rec, res, activation, hangarNodeUID)
}

func runOutputFixtureOnNode(rec *brine.Recorder, res brine.Resources, activation, nodeUID string) (RunOutputStart, error) {
	return runOutputFixtureConfig(rec, res, activation, nodeUID, false)
}

func runOutputFixtureConfig(rec *brine.Recorder, res brine.Resources, activation, nodeUID string, checks bool) (RunOutputStart, error) {
	jdb, err := jetbridgeDBFrom(res)
	if err != nil {
		return RunOutputStart{}, err
	}
	in := RunOutputStart{DB: jdb}
	decl, err := resultDeclaration("one inline producer")
	if err != nil {
		return in, err
	}
	decl.Config.Jobs = append(decl.Config.Jobs, atc.JobConfig{Name: "other", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "other", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}})
	if checks {
		decl.Config.ResourceTypes = atc.ResourceTypes{{Name: "custom", Type: "mock", Source: atc.Source{"uri": "fixture"}}}
		decl.Config.Resources = atc.ResourceConfigs{{Name: "source", Type: "mock", Source: atc.Source{"uri": "fixture"}}}
		decl.Config.Jobs[1].PlanSequence = append([]atc.Step{{Config: &atc.GetStep{Name: "source"}}}, decl.Config.Jobs[1].PlanSequence...)
	}
	team, err := jdb.TeamFactory.CreateTeam(atc.Team{Name: "output-start"})
	if err != nil {
		return in, err
	}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "review"}, decl.Config, 0, false)
	if err != nil {
		return in, err
	}
	opts := db.RunCreationOpts{}
	opts.ActivationEpoch = int64(runActivationEpoch)
	opts.HangarOutput = true
	if err := putOutputPlaneInService(jdb); err != nil {
		return in, err
	}
	epoch := int64(runActivationEpoch)
	if activation == "stale" {
		epoch++
	}
	if _, err := db.ReconcilePipelineRunActivation(context.Background(), jdb.Conn, activationEpochUnless(activation == "disabled", epoch)); err != nil {
		return in, err
	}
	tx, err := jdb.Conn.Begin()
	if err != nil {
		return in, err
	}
	defer db.Rollback(tx)
	in.Creation, in.Err = db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory).CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "brine", opts)
	if in.Err != nil {
		return in, nil
	}
	if err := tx.Commit(); err != nil {
		return in, err
	}
	if in.Creation.EntryBuilds[0].JobName() != decl.Config.Jobs[0].Name {
		in.Creation.EntryBuilds[0], in.Creation.EntryBuilds[1] = in.Creation.EntryBuilds[1], in.Creation.EntryBuilds[0]
	}
	task := in.Creation.Config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	in.Plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}
	in.Daemon, err = startHangarDaemonOnNode(rec, nodeUID, true)
	return in, err
}

func (in RunOutputStart) start(plan atc.TaskPlan, node, uid string, rollback bool) (runCaptureRecord, error) {
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := in.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return runCaptureRecord{}, err
	}
	defer db.Rollback(tx)
	started, err := factory.StartRunCapture(ctx, tx, in.Creation.EntryBuilds[0].ID(), plan, time.Hour, node, uid)
	if err != nil || rollback {
		return runCaptureRecordOf(started.Capture), err
	}
	return runCaptureRecordOf(started.Capture), tx.Commit()
}

// readCapture is the producer's capture as the Run reads it back.
func (in RunOutputStart) readCapture() (runCaptureRecord, error) {
	tx, err := in.DB.Conn.Begin()
	if err != nil {
		return runCaptureRecord{}, err
	}
	defer db.Rollback(tx)
	capture, found, err := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory).RunCaptureTask(context.Background(), tx, in.Creation.EntryBuilds[0].ID(), in.Plan.TaskID)
	if err == nil && !found {
		err = fmt.Errorf("the Run retains no capture for its producer")
	}
	return runCaptureRecordOf(capture.Capture), err
}

func checkRunOutputStart(in RunOutputStart) error {
	if in.Err != nil {
		return in.Err
	}
	if in.Record.State != output.CapturePending {
		return fmt.Errorf("start admission left the capture %s, not pending", in.Record.State)
	}
	if in.Replay.Key.ExecutionID != "" && (in.Replay.Key != in.Record.Key || in.Replay.Execution != in.Record.Execution) {
		return fmt.Errorf("replay changed the exact execution")
	}
	var count int
	if err := in.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_captures s JOIN hangar_captures h USING(execution_id, output_name)
 WHERE s.run_id=$1 AND s.build_id=$2 AND s.task_id=$3 AND s.result_name='findings' AND h.node=$4 AND h.node_uid=$5
 AND h.state='pending' AND s.execution_id=$6`, in.Creation.Run.ID(), in.Creation.EntryBuilds[0].ID(), in.Plan.TaskID,
		brineCaptureNode, hangarNodeUID, string(in.Record.Key.ExecutionID)).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("the capture is not bound to the producing Run and build")
	}
	return nil
}

func checkNoRunOutputStart(in RunOutputStart) error {
	var links, captures int
	if err := in.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_captures), (SELECT count(*) FROM hangar_captures)`).Scan(&links, &captures); err != nil {
		return err
	}
	if links != 0 || captures != 0 {
		return fmt.Errorf("uncommitted start left %d capture links and %d capture rows", links, captures)
	}
	return nil
}

// activationEpochUnless is the Run activation epoch a scenario configures, or
// zero -- admission off -- when disabled is set.
func activationEpochUnless(disabled bool, epoch int64) int64 {
	if disabled {
		return 0
	}
	return epoch
}
