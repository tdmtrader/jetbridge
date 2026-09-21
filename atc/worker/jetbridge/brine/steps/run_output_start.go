package steps

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// These interfaces let the red feature describe the missing production seam
// without implementing a substitute for it.
type runOutputStarter interface {
	PredeclareOutputTask(context.Context, db.Tx, int, atc.TaskPlan, int64, time.Duration, string, string) (output.HandoffRecord, error)
	RecordOutputSource(context.Context, db.Tx, int, atc.TaskPlan, output.ReservedIncarnation, string) error
}

type RunOutputStart struct {
	DB             JetbridgeDB
	Creation       db.RunCreation
	Plan           atc.TaskPlan
	Daemon         HangarDaemon
	Record, Replay output.HandoffRecord
	Err            error
}

func RunOutputStartDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, RunOutputStart]("two controllers concurrently admit the producer", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.DB.Conn.SetMaxOpenConns(4)
			var records [2]output.HandoffRecord
			var errs [2]error
			var group sync.WaitGroup
			start := make(chan struct{})
			for i := range records {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					<-start
					records[i], errs[i] = in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
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
		brine.DefineMap[RunOutputStart, RunOutputStart]("its build tries to finish before output disposition", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Err = in.Creation.EntryBuilds[0].Finish(db.BuildStatusSucceeded)
			return in, nil
		}),
		CheckThat[RunOutputStart]("the build remains unfinished and the handoff is retained", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("the producer became externally terminal before output disposition")
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
		brine.DefineMap[RunOutputStart, RunOutputStart]("deletion of its start token is attempted", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			_, in.Err = in.DB.Conn.Exec(`DELETE FROM pipeline_run_output_starts WHERE run_id=$1`, in.Creation.Run.ID())
			return in, nil
		}),
		CheckThat[RunOutputStart]("the original start remains immutable", func(in RunOutputStart) error {
			// Only the team's purge may delete a start identity; anything else
			// is refused by the start's own trigger.
			if in.Err == nil || !strings.Contains(in.Err.Error(), "Run output start identity is deleted only by its team's purge") {
				return fmt.Errorf("start identity was removable: %v", in.Err)
			}
			in.Err = nil
			return checkRunOutputStart(in)
		}),
		CheckThat[RetainedRunDefinition]("the Run records an immutable legacy birth contract", func(in RetainedRunDefinition) error {
			var version string
			var epoch *int64
			if err := in.DB.Conn.QueryRow(`SELECT run_contract_version, activation_epoch FROM pipeline_runs WHERE id=$1`, in.Creation.Run.ID()).Scan(&version, &epoch); err != nil {
				return err
			}
			if version != "legacy_v1" || epoch != nil {
				return fmt.Errorf("legacy birth was misclassified: %q %v", version, epoch)
			}
			_, err := in.DB.Conn.Exec(`UPDATE pipeline_runs SET run_contract_version='v2', activation_epoch=1 WHERE id=$1`, in.Creation.Run.ID())
			if err == nil || !strings.Contains(err.Error(), "immutable") {
				return fmt.Errorf("birth contract was mutable: %v", err)
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
			in.Record, in.Err = in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("a new controller repeats the producer admission", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Replay, in.Err = in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its producer admission transaction is rolled back", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			in.Record, in.Err = in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, true)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("its producer asks to start with a different {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			fact, _ := p.GetString(0)
			plan := in.Plan
			selected := *plan.RunResult
			plan.RunResult = &selected
			epoch := int64(hangarEpoch)
			var err error
			switch fact {
			case "task":
				plan.TaskID = freshUUID()
			case "result":
				plan.RunResult.Name = "other"
			case "output":
				plan.RunResult.Output = "other"
			case "epoch":
				epoch++
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
			_, in.Err = in.start(plan, epoch, "brine-node", hangarNodeUID, false)
			return in, nil
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("another node is offered for that producer", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			_, in.Err = in.start(in.Plan, int64(hangarEpoch), "replacement-node", "replacement-uid", false)
			return in, nil
		}),
		CheckThat[RunOutputStart]("one non-authorizing handoff belongs to that exact Run and build", checkRunOutputStart),
		CheckThat[RunOutputStart]("neither a start token nor a Hangar handoff remains", checkNoRunOutputStart),
		CheckThat[RunOutputStart]("start admission is refused without a handoff", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("unadmitted producer was allowed to start")
			}
			return checkNoRunOutputStart(in)
		}),
		CheckThat[RunOutputStart]("the original node and handoff remain authoritative", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("another node replaced the admitted producer")
			}
			in.Err = nil
			return checkRunOutputStart(in)
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("the selected daemon reserves the producer source", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			return reserveRunSource(in, false)
		}),
		brine.DefineMap[RunOutputStart, RunOutputStart]("a replacement node answers the source reservation", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunOutputStart, error) {
			return reserveRunSource(in, true)
		}),
		CheckThat[RunOutputStart]("reconnecting recovers that exact daemon-issued location", func(in RunOutputStart) error {
			if in.Err != nil {
				return in.Err
			}
			record, err := in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			if err != nil {
				return err
			}
			if !record.Source.Reserved() || record.Source != in.Replay.Source {
				return fmt.Errorf("source reservation was not retained")
			}
			return nil
		}),
		CheckThat[RunOutputStart]("the source answer is refused without changing start admission", func(in RunOutputStart) error {
			if in.Err == nil {
				return fmt.Errorf("replacement node supplied an admitted source")
			}
			record, err := in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			if err != nil {
				return err
			}
			if record.Source.Reserved() {
				return fmt.Errorf("refused source was persisted")
			}
			return nil
		}),
	}
}

func runOutputFixture(rec *brine.Recorder, res brine.Resources, activation string) (RunOutputStart, error) {
	return runOutputFixtureOnNode(rec, res, activation, hangarNodeUID)
}

func runOutputFixtureOnNode(rec *brine.Recorder, res brine.Resources, activation, nodeUID string) (RunOutputStart, error) {
	return runOutputFixtureConfig(rec, res, activation, nodeUID, false)
}

func runOutputFixtureConfig(rec *brine.Recorder, res brine.Resources, activation, nodeUID string, checks bool, cohort ...string) (RunOutputStart, error) {
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
	opts.ActivationEpoch = int64(hangarEpoch)
	if err := openActivationEpoch(jdb, cohort...); err != nil {
		return in, err
	}
	epoch := int64(hangarEpoch)
	if activation == "stale" {
		epoch++
	}
	if _, err := jdb.Conn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=$2 WHERE singleton`, epoch, activation != "disabled"); err != nil {
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

func (in RunOutputStart) start(plan atc.TaskPlan, epoch int64, node, uid string, rollback bool) (output.HandoffRecord, error) {
	factory, ok := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory).(runOutputStarter)
	if !ok {
		return output.HandoffRecord{}, fmt.Errorf("Run does not own producer start admission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := in.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return output.HandoffRecord{}, err
	}
	defer db.Rollback(tx)
	record, err := factory.PredeclareOutputTask(ctx, tx, in.Creation.EntryBuilds[0].ID(), plan, epoch, time.Hour, node, uid)
	if err != nil || rollback {
		return record, err
	}
	return record, tx.Commit()
}

func checkRunOutputStart(in RunOutputStart) error {
	if in.Err != nil {
		return in.Err
	}
	if in.Record.Disposition != nil || in.Record.HoldAcknowledged || in.Record.Source.Reserved() {
		return fmt.Errorf("start admission authorized more than predeclaration")
	}
	if in.Replay.HandoffID != "" && !reflect.DeepEqual(in.Record, in.Replay) {
		return fmt.Errorf("replay changed the exact execution")
	}
	var count int
	if err := in.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id) WHERE s.run_id=$1 AND s.build_id=$2 AND s.task_id=$3 AND s.result_name='findings' AND s.node_name='brine-node' AND s.node_uid='brine-node-1' AND h.handoff_id=$4`, in.Creation.Run.ID(), in.Creation.EntryBuilds[0].ID(), in.Plan.TaskID, string(in.Record.HandoffID)).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("start is not bound to the producing Run and build")
	}
	return nil
}

func checkNoRunOutputStart(in RunOutputStart) error {
	var tokens, handoffs int
	if err := in.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_output_starts), (SELECT count(*) FROM hangar_handoff_predeclarations)`).Scan(&tokens, &handoffs); err != nil {
		return err
	}
	if tokens != 0 || handoffs != 0 {
		return fmt.Errorf("uncommitted start left %d tokens and %d handoffs", tokens, handoffs)
	}
	return nil
}

func reserveRunSource(in RunOutputStart, replacement bool) (RunOutputStart, error) {
	dispatch, err := in.DB.Conn.Begin()
	if err != nil {
		return in, err
	}
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	if err := factory.RequestOutputSource(context.Background(), dispatch, in.Creation.EntryBuilds[0].ID(), in.Plan, int64(hangarEpoch)); err != nil {
		db.Rollback(dispatch)
		return in, err
	}
	if err := dispatch.Commit(); err != nil {
		return in, err
	}
	client := jetbridge.NewOutputControlClient(in.Daemon.Output.URL, in.Daemon.HTTP, in.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	r := in.Record
	if _, err := client.Admit(context.Background(), executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: r.Execution, ActivationEpoch: r.ActivationEpoch, NodeUID: hangarNodeUID, Capability: "brine-base-control"}); err != nil {
		return in, err
	}
	reserved, err := client.ReserveIncarnation(context.Background(), output.CaptureAdmission{ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline})
	if err != nil {
		return in, err
	}
	if replacement {
		reserved.Incarnation.NodeUID = "replacement-uid"
	}
	tx, err := in.DB.Conn.Begin()
	if err != nil {
		return in, err
	}
	defer db.Rollback(tx)
	in.Err = factory.RecordOutputSource(context.Background(), tx, in.Creation.EntryBuilds[0].ID(), in.Plan, reserved, "brine-node")
	if in.Err != nil {
		return in, nil
	}
	if err := tx.Commit(); err != nil {
		return in, err
	}
	in.Replay, err = in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
	return in, err
}
