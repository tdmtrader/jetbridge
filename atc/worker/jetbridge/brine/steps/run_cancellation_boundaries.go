package steps

import (
	"context"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
)

type RunCheckAdmission struct {
	Start RunOutputStart
	Build db.Build
	Err   error
}

func RunCancellationBoundaryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunCheckAdmission, RunCheckAdmission]("all its Run jobs fail while the check remains active", func(in RunCheckAdmission, _ brine.Params, _ *brine.Recorder) (RunCheckAdmission, error) {
			if in.Err != nil {
				return in, in.Err
			}
			for _, b := range in.Start.Creation.EntryBuilds {
				if err := b.Finish(db.BuildStatusFailed); err != nil {
					return in, err
				}
			}
			if err := consumeRunScheduling(in.Start); err != nil {
				return in, err
			}
			result := finalizeRunResult(RunResultPublication{Start: in.Start}, false)
			return in, result.Err
		}),
		CheckThat[RunCheckAdmission]("that active check prevents terminal Run publication", func(in RunCheckAdmission) error {
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			_, found, err := factory.TerminalResult(context.Background(), in.Start.Creation.Run.ID())
			if err != nil {
				return err
			}
			if found {
				return fmt.Errorf("Run completed while its check was active")
			}
			return nil
		}),
		brine.DefineMap[RunCheckAdmission, RunCheckAdmission]("its owned resource check finishes with an error", func(in RunCheckAdmission, _ brine.Params, _ *brine.Recorder) (RunCheckAdmission, error) {
			return in, in.Build.Finish(db.BuildStatusErrored)
		}),
		CheckThat[RunCheckAdmission]("only the Run jobs determine the terminal outcome", func(in RunCheckAdmission) error {
			result := finalizeRunResult(RunResultPublication{Start: in.Start}, false)
			return checkRunResult(result, "failed", false)
		}),

		brine.DefineMapUsing[brine.Empty, RunOutputStart]("a v2 result Run with resource checks", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputStart, error) {
			return runOutputFixtureConfig(rec, res, "current", hangarNodeUID, true)
		}),
		brine.DefineMap[RunCancellation, RunCheckAdmission]("the cancelled Run requests a {string} resource check", func(in RunCancellation, p brine.Params, _ *brine.Recorder) (RunCheckAdmission, error) {
			if in.Err != nil {
				return RunCheckAdmission{}, in.Err
			}
			kind, _ := p.GetString(0)
			return requestRunCheck(in.Result.Start, kind)
		}),
		brine.DefineMap[RunOutputStart, RunCheckAdmission]("the running Run requests a {string} resource check", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (RunCheckAdmission, error) {
			kind, _ := p.GetString(0)
			return requestRunCheck(in, kind)
		}),
		CheckThat[RunCheckAdmission]("no check is admitted into the cancelled Run", func(in RunCheckAdmission) error {
			if in.Err == nil {
				return fmt.Errorf("cancelled Run admitted a resource check")
			}
			var count int
			err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM builds WHERE pipeline_id=$1 AND (resource_id IS NOT NULL OR resource_type_id IS NOT NULL)`, in.Start.Creation.EntryBuilds[0].PipelineID()).Scan(&count)
			if err != nil {
				return err
			}
			if count != 0 || in.Build != nil {
				return fmt.Errorf("refused check left an execution")
			}
			return nil
		}),
		CheckThat[RunCheckAdmission]("the check is durably owned by its Run without job result identity", func(in RunCheckAdmission) error {
			if in.Err != nil {
				return in.Err
			}
			if in.Build == nil {
				return fmt.Errorf("check not admitted")
			}
			var count int
			err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM builds WHERE id=$1 AND pipeline_run_id=$2 AND run_job_name IS NULL AND run_job_key IS NULL AND (resource_id IS NOT NULL OR resource_type_id IS NOT NULL) AND status='started'`, in.Build.ID(), in.Start.Creation.Run.ID()).Scan(&count)
			if err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("admitted check lacks durable Run identity")
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("the whole Run is cancelled before capture selection", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			_, err := acceptRunCancellation(in.Start, "first-owner", nil, false)
			return in, err
		}),
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("the whole Run is cancelled after publication", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			_, err := acceptRunCancellation(in.Finish.Start, "first-owner", nil, false)
			return in, err
		}),
		CheckThat[RunOutputCandidate]("the cancelled Run retains a discard without a candidate claim", func(in RunOutputCandidate) error {
			claims, record, err := in.claims()
			if err != nil {
				return err
			}
			if len(claims) != 0 || !record.Settled {
				return fmt.Errorf("cancelled Run retained a new result candidate or lost settlement")
			}
			var reason string
			err = in.Finish.Start.DB.Conn.QueryRow(`SELECT reason FROM pipeline_run_output_discards WHERE handoff_id=$1`, string(in.Record.HandoffID)).Scan(&reason)
			if err != nil {
				return err
			}
			if reason != "run_cancelled" {
				return fmt.Errorf("discard lost its Run cancellation reason")
			}
			return nil
		}),
		CheckThat[RunCancellation]("direct changes cannot remove or replace the cancellation request", func(in RunCancellation) error {
			if in.Err != nil {
				return in.Err
			}
			for _, q := range []string{`UPDATE pipeline_runs SET cancel_requested_at=NULL,cancel_requested_by=NULL,cancel_reason=NULL WHERE id=$1`, `UPDATE pipeline_runs SET cancel_requested_at=clock_timestamp()+interval '1 second' WHERE id=$1`, `UPDATE pipeline_runs SET cancel_requested_by='replacement' WHERE id=$1`, `UPDATE pipeline_runs SET cancel_reason='replacement' WHERE id=$1`} {
				if _, err := in.Result.Start.DB.Conn.Exec(q, in.Result.Start.Creation.Run.ID()); err == nil {
					return fmt.Errorf("direct mutation changed a cancellation fact")
				}
			}
			return nil
		}),
	}
}

func requestRunCheck(in RunOutputStart, kind string) (RunCheckAdmission, error) {
	out := RunCheckAdmission{Start: in}
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	pipeline, found, err := factory.InstancePipeline(in.Creation.Run)
	if err != nil {
		return out, err
	}
	if !found {
		return out, fmt.Errorf("missing payload")
	}
	resource, found, err := pipeline.Resource("source")
	if err != nil {
		return out, err
	}
	if !found {
		return out, fmt.Errorf("missing resource")
	}
	plan := atc.Plan{ID: "check", Check: &atc.CheckPlan{Type: "mock", Resource: "source", Source: atc.Source{"uri": "fixture"}}}
	switch kind {
	case "scanner":
		queue := make(chan db.Build, 1)
		factory := db.NewCheckFactory(in.DB.Conn, in.DB.LockFactory, queue, util.NewSequenceGenerator(0))
		out.Build, _, out.Err = factory.TryCreateCheck(context.Background(), resource, nil, nil, true, false, false)
		if len(queue) > 0 && out.Build != nil && out.Build.ID() != 0 {
			return out, fmt.Errorf("durable check was also enqueued in memory")
		}
	case "type":
		rt, found, err := pipeline.ResourceType("custom")
		if err != nil {
			return out, err
		}
		if !found {
			return out, fmt.Errorf("missing resource type")
		}
		plan.Check.Resource = ""
		plan.Check.ResourceType = "custom"
		out.Build, _, out.Err = rt.CreateBuild(context.Background(), true, plan)
	case "persisted":
		out.Build, _, out.Err = resource.CreateBuild(context.Background(), true, plan)
	case "in-memory":
		out.Build, out.Err = resource.CreateInMemoryBuild(context.Background(), plan, util.NewSequenceGenerator(0))
	default:
		return out, fmt.Errorf("unknown check kind")
	}
	return out, nil
}
