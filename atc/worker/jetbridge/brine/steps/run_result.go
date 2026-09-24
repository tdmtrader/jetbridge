package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// This is a capability assertion, not a substitute implementation: the red
// feature reports the absent production operation against the real factory.
type runResultFinalizer interface {
	FinalizeOutputRun(context.Context, db.Tx, int) (bool, error)
}

type RunResultPublication struct {
	Start            RunOutputStart
	Candidate        *RunOutputCandidate
	Completed        bool
	Err              error
	Snapshot         []byte
	SupersededClaims []output.ClaimID
}

func RunResultDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunResultPublication, RunResultPublication]("its disposable payload is reclaimed after a newer Run", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			team, found, err := in.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing fixture team")
			}
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			definition, found, err := factory.Definition(in.Start.Creation.Run.ID())
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing retained definition")
			}
			keep := 1
			definition.Template.RunRetention = &atc.RunRetentionConfig{KeepLast: &keep}
			previous, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing base template")
			}
			template, _, err := team.SavePipeline(atc.PipelineRef{Name: "review"}, definition.Template, previous.ConfigVersion(), false)
			if err != nil {
				return in, err
			}
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "brine-newer", db.RunCreationOpts{ActivationEpoch: int64(hangarEpoch), HangarEpoch: int64(hangarEpoch)})
			if err == nil {
				err = tx.Commit()
			}
			db.Rollback(tx)
			if err != nil {
				return in, err
			}
			removed, err := db.NewPipelineRunReclaimLifecycle(in.Start.DB.Conn).DestroyReclaimableRun(in.Start.Creation.Run.ID())
			if err != nil {
				return in, err
			}
			if !removed {
				return in, fmt.Errorf("eligible disposable payload was not reclaimed")
			}
			_, found, err = factory.InstancePipeline(in.Start.Creation.Run)
			if err != nil {
				return in, err
			}
			if found {
				return in, fmt.Errorf("payload remains")
			}
			return in, nil
		}),
		CheckThat[RunResultPublication]("the header reader returns the retained result and version", func(in RunResultPublication) error {
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			result, found, err := factory.TerminalResult(context.Background(), in.Start.Creation.Run.ID())
			if err != nil {
				return err
			}
			if !found || result.Status != atc.RunStatusSucceeded || result.CompletedAt.IsZero() || result.Version == "" || len(result.Results) != 1 {
				return fmt.Errorf("header result reader lost the terminal observation")
			}
			return checkRunResult(in, "succeeded", true)
		}),

		brine.DefineMap[RunResultPublication, RunResultPublication]("a fresh capture component finalizes pending Runs", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			finalizer := runs.ResultFinalizer{Conn: in.Start.DB.Conn, Factory: db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)}
			in.Err = finalizer.Run(context.Background())
			return in, nil
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("the selected job reruns with {string}", func(in RunResultPublication, p brine.Params, rec *brine.Recorder) (RunResultPublication, error) {
			capture, _ := p.GetString(0)
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			payload, found, err := factory.InstancePipeline(in.Start.Creation.Run)
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing payload")
			}
			old := in.Start.Creation.EntryBuilds[0]
			job, found, err := payload.Job(old.JobName())
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing producer job")
			}
			build, err := job.RerunBuild(old, "brine-rerun")
			if err != nil {
				return in, err
			}
			if capture == "no candidate" {
				return in, build.Finish(db.BuildStatusSucceeded)
			}
			claims, _, err := in.Candidate.claims()
			if err != nil {
				return in, err
			}
			for _, c := range claims {
				if c.Active() {
					in.SupersededClaims = append(in.SupersededClaims, c.ClaimID)
				}
			}
			runtime := in.Candidate.Runtime
			runtime.Start.Creation.EntryBuilds = append([]db.Build(nil), runtime.Start.Creation.EntryBuilds...)
			runtime.Start.Creation.EntryBuilds[0] = build
			candidate, err := publishRunCandidate(runtime, rec)
			if err != nil {
				return in, err
			}
			candidate.Finish.Release, err = candidate.Finish.daemonRelease()
			if err != nil {
				return in, err
			}
			if err := candidate.Finish.recordRelease(candidate.Finish.Release, false); err != nil {
				return in, err
			}
			in.Candidate = &candidate
			return in, build.Finish(db.BuildStatusSucceeded)
		}),
		CheckThat[RunResultPublication]("superseded result claims are released", func(in RunResultPublication) error {
			if len(in.SupersededClaims) == 0 {
				return fmt.Errorf("no superseded fixture claim")
			}
			for _, id := range in.SupersededClaims {
				var active bool
				if err := in.Start.DB.Conn.QueryRow(`SELECT released_at IS NULL FROM hangar_claims WHERE claim_id=$1`, string(id)).Scan(&active); err != nil {
					return err
				}
				if active {
					return fmt.Errorf("superseded claim %s remains active", id)
				}
			}
			return nil
		}),
		CheckThat[RunResultPublication]("late build completion cannot change the selected outcomes", func(in RunResultPublication) error {
			b := in.Start.Creation.EntryBuilds[1]
			if err := b.Finish(db.BuildStatusFailed); err == nil {
				return fmt.Errorf("terminal Run accepted a changed build outcome")
			}
			var status string
			if err := in.Start.DB.Conn.QueryRow(`SELECT status FROM builds WHERE id=$1`, b.ID()).Scan(&status); err != nil {
				return err
			}
			if status != "succeeded" {
				return fmt.Errorf("late completion changed the build")
			}
			return nil
		}),
		CheckThat[RunResultPublication]("a generic release cannot discard its protected candidate claim", func(in RunResultPublication) error {
			if in.Candidate == nil {
				return fmt.Errorf("no fixture candidate")
			}
			claims, _, err := in.Candidate.claims()
			if err != nil {
				return err
			}
			for _, c := range claims {
				if c.Active() {
					tx, err := in.Start.DB.Conn.Begin()
					if err != nil {
						return err
					}
					defer db.Rollback(tx)
					err = in.Candidate.Finish.repository().ReleaseClaim(context.Background(), tx, output.ClaimRelease{ProtocolVersion: output.ProtocolVersion, ClaimID: c.ClaimID, Ref: c.Ref, RequestedAt: output.NewTimestamp(time.Now().UTC())})
					if err == nil {
						err = tx.Commit()
					}
					if err == nil {
						return fmt.Errorf("generic release dropped a Run-owned claim")
					}
					return nil
				}
			}
			return fmt.Errorf("no active claim to test")
		}),

		brine.DefineMap[RunOutputCandidate, RunResultPublication]("its aggregate Run result is inspected", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			return RunResultPublication{Start: in.Finish.Start, Candidate: &in}, in.Err
		}),
		brine.DefineMap[RunOutputStart, RunResultPublication]("its uncaptured result producer finishes successfully", func(in RunOutputStart, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			return RunResultPublication{Start: in}, in.Creation.EntryBuilds[0].Finish(db.BuildStatusSucceeded)
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("its other Run jobs finish as {string}", func(in RunResultPublication, p brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			status, _ := p.GetString(0)
			for _, b := range in.Start.Creation.EntryBuilds[1:] {
				if err := b.Finish(db.BuildStatus(status)); err != nil {
					return in, err
				}
			}
			return in, nil
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("its scheduler consumes all requested Run work", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			return in, consumeRunScheduling(in.Start)
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("the aggregate Run completion is attempted", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			return finalizeRunResult(in, false), nil
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("the aggregate Run completion is rolled back", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			return finalizeRunResult(in, true), nil
		}),
		brine.DefineMap[RunResultPublication, RunResultPublication]("the terminal Run observation is remembered", func(in RunResultPublication, _ brine.Params, _ *brine.Recorder) (RunResultPublication, error) {
			var err error
			in.Snapshot, err = readResultSnapshot(in)
			return in, err
		}),
		CheckThat[RunResultPublication]("the exact candidate is the complete successful Run result", func(in RunResultPublication) error { return checkRunResult(in, "succeeded", true) }),
		brine.DefineCheck[RunResultPublication]("the Run publishes {string} with an explicit empty result", func(in RunResultPublication, p brine.Params, _ *brine.Recorder) error {
			status, _ := p.GetString(0)
			return checkRunResult(in, status, false)
		}),
		CheckThat[RunResultPublication]("the aggregate Run remains running with no public result", func(in RunResultPublication) error {
			if in.Err != nil {
				return in.Err
			}
			var status string
			var body []byte
			var version *string
			var completed *time.Time
			err := in.Start.DB.Conn.QueryRow(`SELECT status,completed_at,result_manifest,terminal_observation_version FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&status, &completed, &body, &version)
			if err != nil {
				return err
			}
			if status != "running" || completed != nil || len(body) != 0 || version != nil {
				return fmt.Errorf("incomplete Run exposed a terminal observation: %s %s", status, body)
			}
			if in.Candidate != nil {
				claims, _, err := in.Candidate.claims()
				if err != nil {
					return err
				}
				if len(claims) != 1 || !claims[0].Active() {
					return fmt.Errorf("incomplete Run lost its candidate claim")
				}
			}
			return nil
		}),
		CheckThat[RunResultPublication]("replaying completion preserves the same terminal observation", func(in RunResultPublication) error {
			if in.Err != nil {
				return in.Err
			}
			body, err := readResultSnapshot(in)
			if err != nil {
				return err
			}
			if len(in.Snapshot) == 0 || !reflect.DeepEqual(in.Snapshot, body) {
				return fmt.Errorf("terminal observation changed on replay")
			}
			return nil
		}),
		CheckThat[RunResultPublication]("direct changes to its published result are refused", func(in RunResultPublication) error {
			for _, q := range []string{`UPDATE pipeline_runs SET result_manifest='{}' WHERE id=$1`, `UPDATE pipeline_runs SET terminal_observation_version='changed' WHERE id=$1`, `UPDATE pipeline_runs SET completed_at=completed_at+interval '1 second' WHERE id=$1`, `UPDATE pipeline_runs SET status='failed' WHERE id=$1`} {
				if _, err := in.Start.DB.Conn.Exec(q, in.Start.Creation.Run.ID()); err == nil {
					return fmt.Errorf("terminal publication was mutable: %s", q)
				}
			}
			return nil
		}),
	}
}

func consumeRunScheduling(in RunOutputStart) error {
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	payload, found, err := factory.InstancePipeline(in.Creation.Run)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("missing Run payload")
	}
	for _, config := range in.Creation.Config.Jobs {
		job, found, err := payload.Job(config.Name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing job %s", config.Name)
		}
		if err := job.ConsumeScheduleRequest(time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func finalizeRunResult(in RunResultPublication, rollback bool) RunResultPublication {
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	finalizer, ok := factory.(runResultFinalizer)
	if !ok {
		in.Err = fmt.Errorf("Run has no aggregate result finalizer")
		return in
	}
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		in.Err = err
		return in
	}
	defer db.Rollback(tx)
	in.Completed, in.Err = finalizer.FinalizeOutputRun(context.Background(), tx, in.Start.Creation.Run.ID())
	if in.Err == nil && !rollback {
		in.Err = tx.Commit()
	}
	return in
}

func readResultSnapshot(in RunResultPublication) ([]byte, error) {
	var body []byte
	err := in.Start.DB.Conn.QueryRow(`SELECT jsonb_build_object('status',status,'completed_at',completed_at,'results',result_manifest,'version',terminal_observation_version) FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&body)
	return body, err
}

func checkRunResult(in RunResultPublication, status string, selected bool) error {
	if in.Err != nil {
		return in.Err
	}
	var actual string
	var completed *time.Time
	var version *string
	var body []byte
	err := in.Start.DB.Conn.QueryRow(`SELECT status,completed_at,result_manifest,terminal_observation_version FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&actual, &completed, &body, &version)
	if err != nil {
		return err
	}
	if actual != status || completed == nil || version == nil || *version == "" {
		return fmt.Errorf("Run publication incomplete: status=%s completed=%v version=%v", actual, completed, version)
	}
	var results map[string]struct {
		Ref     hangar.TreeRef `json:"ref"`
		ClaimID output.ClaimID `json:"claim_id"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return err
	}
	if results == nil {
		return fmt.Errorf("result map is not explicit")
	}
	if selected {
		if in.Candidate == nil {
			return fmt.Errorf("missing fixture candidate")
		}
		result, found := results[in.Start.Plan.RunResult.Name]
		claims, _, err := in.Candidate.claims()
		if err != nil {
			return err
		}
		if !found || len(results) != 1 || result.Ref != in.Candidate.Record.Ref || activeResultClaims(claims, result.ClaimID) != 1 {
			return fmt.Errorf("Run did not retain the exact selected candidate and claim: %s", body)
		}
	} else {
		if len(results) != 0 {
			return fmt.Errorf("non-success exposed partial results: %s", body)
		}
		if in.Candidate != nil {
			claims, _, err := in.Candidate.claims()
			if err != nil {
				return err
			}
			for _, c := range claims {
				if c.Active() {
					return fmt.Errorf("failed Run retained candidate claim %s", c.ClaimID)
				}
			}
		}
	}
	return nil
}

func activeResultClaims(claims []output.ClaimRecord, selected output.ClaimID) int {
	active := 0
	found := false
	for _, c := range claims {
		if c.Active() {
			active++
			if c.ClaimID == selected {
				found = true
			}
		}
	}
	if !found {
		return 0
	}
	return active
}
