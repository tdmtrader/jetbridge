package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// The real factory must supply this operation; no replacement implementation is used.
type runCancellationAcceptor interface {
	AcceptRunCancellation(context.Context, db.Tx, int, string, *string) (atc.RunCancelOutcome, error)
}

type RunCancellation struct {
	Result   RunResultPublication
	Outcome  atc.RunCancelOutcome
	Err      error
	Snapshot []byte
	Reason   string
	Started  time.Time
}

func RunCancellationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, RunCancellation]("whole-Run cancellation is requested by {string} for {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			by, _ := p.GetString(0)
			reason, _ := p.GetString(1)
			out := RunCancellation{Result: RunResultPublication{Start: in}, Reason: reason}
			if err := in.DB.Conn.QueryRow(`SELECT clock_timestamp()`).Scan(&out.Started); err != nil {
				return out, err
			}
			out.Outcome, out.Err = acceptRunCancellation(in, by, &reason, false)
			return out, nil
		}),
		brine.DefineMap[RunCancellation, RunCancellation]("whole-Run cancellation is repeated by {string} for {string}", func(in RunCancellation, p brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			var err error
			in.Snapshot, err = readCancellationSnapshot(in.Result.Start)
			if err != nil {
				return in, err
			}
			by, _ := p.GetString(0)
			reason, _ := p.GetString(1)
			in.Outcome, in.Err = acceptRunCancellation(in.Result.Start, by, &reason, false)
			return in, nil
		}),
		brine.DefineCheck[RunCancellation]("the Run cancellation outcome is {string}", func(in RunCancellation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if in.Err != nil {
				return in.Err
			}
			if string(in.Outcome) != want {
				return fmt.Errorf("cancellation outcome %q, want %q", in.Outcome, want)
			}
			return nil
		}),
		CheckThat[RunCancellation]("cancellation records its first request without stopping a build", func(in RunCancellation) error {
			var at time.Time
			var by, reason, status string
			var stopped int
			var now time.Time
			err := in.Result.Start.DB.Conn.QueryRow(`SELECT cancel_requested_at,cancel_requested_by,cancel_reason,status,clock_timestamp(),(SELECT count(*) FROM builds WHERE pipeline_run_id=r.id AND (aborted OR completed)) FROM pipeline_runs r WHERE id=$1`, in.Result.Start.Creation.Run.ID()).Scan(&at, &by, &reason, &status, &now, &stopped)
			if err != nil {
				return err
			}
			if by != "first-owner" || reason != in.Reason || status != "running" || stopped != 0 || at.Before(in.Started) || at.After(now) {
				return fmt.Errorf("cancellation did not retain its first DB-clock request independently of build stopping")
			}
			return nil
		}),
		CheckThat[RunCancellation]("the original cancellation facts are unchanged", func(in RunCancellation) error {
			body, err := readCancellationSnapshot(in.Result.Start)
			if err != nil {
				return err
			}
			if len(in.Snapshot) == 0 || !bytes.Equal(body, in.Snapshot) {
				return fmt.Errorf("later cancellation changed original facts")
			}
			return nil
		}),
		brine.DefineMap[RunOutputStart, RunCancellation]("the whole-Run cancellation transaction is rolled back", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			out := RunCancellation{Result: RunResultPublication{Start: in}}
			out.Outcome, out.Err = acceptRunCancellation(in, "first-owner", nil, true)
			return out, out.Err
		}),
		CheckThat[RunCancellation]("the Run has no cancellation request and still admits work", func(in RunCancellation) error {
			if err := checkNoCancellation(in.Result.Start); err != nil {
				return err
			}
			job, err := cancellationJob(in.Result.Start)
			if err != nil {
				return err
			}
			_, err = job.CreateBuild("after-rollback")
			return err
		}),
		brine.DefineMap[RunCancellation, RunCancellation]("its cancelled Run attempts {string}", func(in RunCancellation, p brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			if in.Err != nil {
				return in, in.Err
			}
			start := in.Result.Start
			op, _ := p.GetString(0)
			job, err := cancellationJob(start)
			if err != nil {
				return in, err
			}
			// Close the old debt so a new request can be distinguished from no change.
			if err := consumeRunScheduling(start); err != nil {
				return in, err
			}
			in.Snapshot, err = readCancellationWork(start)
			if err != nil {
				return in, err
			}
			switch op {
			case "manual build":
				_, in.Err = job.CreateBuild("after-cancel")
			case "rerun":
				_, in.Err = job.RerunBuild(start.Creation.EntryBuilds[0], "after-cancel")
			case "build start":
				_, in.Err = start.Creation.EntryBuilds[0].Start(atc.Plan{ID: "cancel-test", Task: &start.Plan})
			case "output start":
				_, in.Err = start.start(start.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			case "scheduler debt":
				in.Err = job.RequestSchedule()
			case "unpause":
				factory := db.NewPipelineRunFactory(start.DB.Conn, start.DB.LockFactory)
				payload, found, err := factory.InstancePipeline(start.Creation.Run)
				if err != nil {
					return in, err
				}
				if !found {
					return in, fmt.Errorf("missing payload")
				}
				in.Err = payload.Unpause()
			default:
				return in, fmt.Errorf("unknown cancellation operation %q", op)
			}
			return in, nil
		}),
		CheckThat[RunCancellation]("that cancelled Run operation is refused without admitting work", func(in RunCancellation) error {
			if in.Err == nil {
				return fmt.Errorf("cancelled Run admitted more work")
			}
			now, err := readCancellationWork(in.Result.Start)
			if err != nil {
				return err
			}
			if !bytes.Equal(in.Snapshot, now) {
				return fmt.Errorf("refused operation changed admitted work")
			}
			return nil
		}),
		brine.DefineMap[RunOutputStart, RunCancellation]("cancellation supplies an invalid {string} reason", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			kind, _ := p.GetString(0)
			var reason string
			switch kind {
			case "empty":
			case "too long":
				reason = strings.Repeat("sensitive-", 60)
			case "control":
				reason = "private\nreason"
			case "invalid UTF-8":
				reason = string([]byte{255, 254})
			default:
				return RunCancellation{}, fmt.Errorf("unknown reason case")
			}
			out := RunCancellation{Result: RunResultPublication{Start: in}, Reason: reason}
			out.Outcome, out.Err = acceptRunCancellation(in, "first-owner", &reason, false)
			return out, nil
		}),
		CheckThat[RunCancellation]("cancellation rejects the reason without storing or repeating it", func(in RunCancellation) error {
			if in.Err == nil {
				return fmt.Errorf("invalid reason accepted")
			}
			if in.Reason != "" && strings.Contains(in.Err.Error(), in.Reason) {
				return fmt.Errorf("error exposed private reason")
			}
			return checkNoCancellation(in.Result.Start)
		}),
		brine.DefineCheck[RunCancellation]("its cancellation reason is normalized to {string}", func(in RunCancellation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			var got string
			err := in.Result.Start.DB.Conn.QueryRow(`SELECT cancel_reason FROM pipeline_runs WHERE id=$1`, in.Result.Start.Creation.Run.ID()).Scan(&got)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("reason was not normalized")
			}
			return nil
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("the otherwise complete Run is cancelled", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			outcome, err := acceptRunCancellation(in.Start, "first-owner", nil, false)
			if err == nil && outcome != atc.RunCancelAccepted {
				err = fmt.Errorf("cancel was not accepted")
			}
			return in, err
		}),
		brine.DefineMap[RunResultPublication, RunCancellation]("the completed Run receives a cancellation request", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunCancellation, error) {
			out := RunCancellation{Result: in}
			var err error
			out.Snapshot, err = readResultSnapshot(in)
			if err != nil {
				return out, err
			}
			out.Outcome, out.Err = acceptRunCancellation(in.Start, "first-owner", nil, false)
			return out, nil
		}),
		CheckThat[RunCancellation]("cancellation reports already terminal without changing the observation", func(in RunCancellation) error {
			if in.Err != nil {
				return in.Err
			}
			if in.Outcome != atc.RunCancelAlreadyTerminal {
				return fmt.Errorf("terminal Run accepted cancellation")
			}
			now, err := readResultSnapshot(in.Result)
			if err != nil {
				return err
			}
			if !bytes.Equal(now, in.Snapshot) {
				return fmt.Errorf("cancellation changed completed Run")
			}
			return checkNoCancellation(in.Result.Start)
		}),
	}
}

func acceptRunCancellation(in RunOutputStart, by string, reason *string, rollback bool) (atc.RunCancelOutcome, error) {
	factory, ok := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory).(runCancellationAcceptor)
	if !ok {
		return "", fmt.Errorf("Run has no durable cancellation operation")
	}
	tx, err := in.DB.Conn.Begin()
	if err != nil {
		return "", err
	}
	defer db.Rollback(tx)
	outcome, err := factory.AcceptRunCancellation(context.Background(), tx, in.Creation.Run.ID(), by, reason)
	if err == nil && !rollback {
		err = tx.Commit()
	}
	return outcome, err
}
func readCancellationSnapshot(in RunOutputStart) ([]byte, error) {
	var body []byte
	err := in.DB.Conn.QueryRow(`SELECT jsonb_build_object('at',cancel_requested_at,'by',cancel_requested_by,'reason',cancel_reason,'epoch',activation_epoch) FROM pipeline_runs WHERE id=$1`, in.Creation.Run.ID()).Scan(&body)
	return body, err
}
func readCancellationWork(in RunOutputStart) ([]byte, error) {
	var body []byte
	err := in.DB.Conn.QueryRow(`SELECT jsonb_build_object('builds',(SELECT jsonb_agg(jsonb_build_array(id,status,aborted,completed) ORDER BY id) FROM builds WHERE pipeline_run_id=r.id),'starts',(SELECT count(*) FROM pipeline_run_output_starts WHERE run_id=r.id),'debt',(SELECT jsonb_agg(jsonb_build_array(j.id,j.schedule_requested,j.last_scheduled) ORDER BY j.id) FROM jobs j JOIN pipelines p ON p.id=j.pipeline_id WHERE p.pipeline_run_id=r.id)) FROM pipeline_runs r WHERE id=$1`, in.Creation.Run.ID()).Scan(&body)
	return body, err
}
func checkNoCancellation(in RunOutputStart) error {
	body, err := readCancellationSnapshot(in)
	if err != nil {
		return err
	}
	var facts map[string]any
	if err := json.Unmarshal(body, &facts); err != nil {
		return err
	}
	if facts["at"] != nil || facts["by"] != nil || facts["reason"] != nil {
		return fmt.Errorf("unexpected cancellation request persisted")
	}
	return nil
}
func cancellationJob(in RunOutputStart) (db.Job, error) {
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	payload, found, err := factory.InstancePipeline(in.Creation.Run)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("missing payload")
	}
	job, found, err := payload.Job(in.Creation.EntryBuilds[0].JobName())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("missing job")
	}
	return job, nil
}
