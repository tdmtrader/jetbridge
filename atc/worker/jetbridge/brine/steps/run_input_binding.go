package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

type RunInputAdmission struct {
	Source    RunResultPublication
	Case      string
	Template  db.Pipeline
	Port      runs.Admitter
	Admission runs.Admission
	Inputs    map[string]map[string]any
	Run       runs.Run
	Replayed  bool
	Err       error
}

func RunInputBindingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunResultPublication, RunInputAdmission]("its result is admitted as a named input with {string}", func(source RunResultPublication, p brine.Params, rec *brine.Recorder) (RunInputAdmission, error) {
			previousGate := atc.PipelineRunActivationEpoch
			atc.PipelineRunActivationEpoch = int64(hangarEpoch)
			TrackDisposer(rec, "the input creation gate", func() error { atc.PipelineRunActivationEpoch = previousGate; return nil })
			in := RunInputAdmission{Source: source}
			in.Case, _ = p.GetString(0)
			if source.Err != nil || !source.Completed {
				return in, fmt.Errorf("source Run did not publish: %v", source.Err)
			}
			team, found, err := source.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("source team missing")
			}
			if in.Case == "a foreign team" {
				team, err = source.Start.DB.TeamFactory.CreateTeam(atc.Team{Name: "input-consumer"})
				if err != nil {
					return in, err
				}
			}
			if err := team.UpdateProviderAuth(atc.TeamAuth{"member": {"users": {"local:owner"}}}); err != nil {
				return in, err
			}
			config := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "consume", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "consume", TaskID: freshUUID(), RunInputs: []atc.RunInput{{Name: "change", Input: "source"}}, Config: &atc.TaskConfig{Platform: "linux", Inputs: []atc.TaskInputConfig{{Name: "source"}}, Run: atc.TaskRunConfig{Path: "true"}}}}}}}}
			if in.Case == "an input and a result" {
				task := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
				result := *source.Start.Plan.RunResult
				task.RunResult = &result
				task.Config.Outputs = []atc.TaskOutputConfig{{Name: result.Output}}
			}
			if in.Case == "one source under two names" || in.Case == "one name routed to two slots" {
				task := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
				task.Config.Inputs = append(task.Config.Inputs, atc.TaskInputConfig{Name: "second"})
				name := "change"
				if in.Case == "one source under two names" {
					name = "also-change"
				}
				task.RunInputs = append(task.RunInputs, atc.RunInput{Name: name, Input: "second"})
			}
			if in.Case == "a reclaimed payload" {
				keep := 1
				config.RunRetention = &atc.RunRetentionConfig{KeepLast: &keep}
			}
			in.Template, _, err = team.SavePipeline(atc.PipelineRef{Name: "input-review"}, config, 0, false)
			if err != nil {
				return in, err
			}
			display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
			if err != nil {
				return in, err
			}
			in.Port = runs.NewAdmitter(source.Start.DB.Conn, db.NewPipelineRunFactory(source.Start.DB.Conn, source.Start.DB.LockFactory), source.Start.DB.TeamFactory, display, nil)
			in.Port.SetOutputEpoch(int64(hangarEpoch))
			in.Admission = runs.Admission{Template: runs.TemplateRef{Team: team.Name(), Pipeline: in.Template.PipelineRef()}, Principal: invocationPrincipal("owner"), ContractKey: "named-input"}
			in.Inputs = map[string]map[string]any{"change": {"run_id": source.Start.Creation.Run.ID(), "result": source.Start.Plan.RunResult.Name}}
			if in.Case == "one source under two names" {
				in.Inputs["also-change"] = in.Inputs["change"]
			}
			switch in.Case {
			case "no required input":
				in.Inputs = nil
			case "an undeclared input":
				in.Inputs["unexpected"] = in.Inputs["change"]
			case "an unknown source":
				in.Inputs["change"]["run_id"] = 2147483647
			case "an unknown result":
				in.Inputs["change"]["result"] = "missing"
			case "a raw tree ref":
				in.Inputs["change"] = map[string]any{"ref": source.Candidate.Record.Ref}
			}
			in.Run, in.Replayed, in.Err = in.admitInput(in.Case == "a rolled back admission")
			if in.Err != nil {
				return in, nil
			}
			first := in.Run.ID
			if in.Case == "a reclaimed payload" {
				if err := in.reclaimInputPayload(); err != nil {
					return in, err
				}
			}
			if in.Case == "an unchanged replay" || in.Case == "a changed source on replay" || in.Case == "a rolled back admission" || in.Case == "a reclaimed payload" {
				if in.Case == "a changed source on replay" {
					in.Inputs["change"]["result"] = "different"
				}
				in.Run, in.Replayed, in.Err = in.admitInput(false)
				if (in.Case == "an unchanged replay" || in.Case == "a reclaimed payload") && in.Err == nil && (in.Run.ID != first || !in.Replayed) {
					return in, fmt.Errorf("input replay created a second Run")
				}
				if in.Case == "a rolled back admission" && in.Replayed {
					return in, fmt.Errorf("rolled back input admission left a replay record")
				}
			}
			return in, nil
		}),
		CheckThat[RunInputAdmission]("the named input admission outcome is correct", func(in RunInputAdmission) error {
			var count, number int
			if err := in.Source.Start.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1),last_run_number FROM pipelines WHERE id=$1`, in.Template.ID()).Scan(&count, &number); err != nil {
				return err
			}
			invalid := false
			switch in.Case {
			case "no required input", "an undeclared input", "an unknown source", "an unknown result", "a foreign team", "a raw tree ref":
				invalid = true
			}
			bindingsPerRun := 1
			if in.Case == "one source under two names" {
				bindingsPerRun = 2
			}
			var activeClaims int
			ref := in.Source.Candidate.Record.Ref
			if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id WHERE c.released_at IS NULL AND l.scope=$1 AND l.digest=$2 AND l.generation=$3`, string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&activeClaims); err != nil {
				return err
			}
			if activeClaims != 1+count*bindingsPerRun {
				return fmt.Errorf("input admission leaked or lost a claim: %d active for %d Runs", activeClaims, count)
			}
			if invalid {
				if in.Err == nil || count != 0 || number != 0 {
					return fmt.Errorf("invalid input admitted Run: error=%v count=%d number=%d", in.Err, count, number)
				}
				return nil
			}
			if in.Case == "a changed source on replay" {
				if !errors.Is(in.Err, runs.ErrInvocationConflict) || count != 1 || number != 1 {
					return fmt.Errorf("changed input did not conflict without allocation: %v", in.Err)
				}
				return nil
			}
			if in.Err != nil {
				return in.Err
			}
			wantedRuns := 1
			if in.Case == "a reclaimed payload" {
				wantedRuns = 2
			}
			if count != wantedRuns || number != wantedRuns {
				return fmt.Errorf("input admission did not create exactly one Run")
			}
			var retained int
			if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_inputs WHERE run_id=$1`, in.Run.ID).Scan(&retained); err != nil {
				return err
			}
			if retained != bindingsPerRun {
				return fmt.Errorf("named input aliases changed the binding count")
			}
			var sourceID int
			var name, result string
			var boundRef hangar.TreeRef
			var claim output.ClaimID
			if err := in.Source.Start.DB.Conn.QueryRow(`SELECT name,source_run_id,source_result,scope,digest,generation,claim_id FROM pipeline_run_inputs WHERE run_id=$1 AND name='change'`, in.Run.ID).Scan(&name, &sourceID, &result, &boundRef.Scope, &boundRef.Digest, &boundRef.Generation, &claim); err != nil {
				return err
			}
			if name != "change" || sourceID != in.Source.Start.Creation.Run.ID() || result != in.Source.Start.Plan.RunResult.Name || boundRef != ref {
				return fmt.Errorf("input binding lost its authorized exact source")
			}
			var active bool
			if err := in.Source.Start.DB.Conn.QueryRow(`SELECT released_at IS NULL FROM hangar_claims WHERE claim_id=$1`, string(claim)).Scan(&active); err != nil {
				return err
			}
			if !active {
				return fmt.Errorf("input has no active exact-generation claim")
			}
			if in.Case == "immutable input bindings" {
				for _, query := range []string{`DELETE FROM pipeline_run_inputs WHERE run_id=$1`, `UPDATE pipeline_run_inputs SET source_result='changed' WHERE run_id=$1`} {
					if _, err := in.Source.Start.DB.Conn.Exec(query, in.Run.ID); err == nil {
						return fmt.Errorf("retained input binding was mutable")
					}
				}
			}
			if in.Case == "a generic claim release" {
				prefix, err := db.HangarConsumerPrefixHeld("brine-run-input-release")
				if err != nil {
					return err
				}
				tx, err := in.Source.Start.DB.Conn.Begin()
				if err != nil {
					return err
				}
				defer db.Rollback(tx)
				var id int
				if err := tx.QueryRow(`SELECT id FROM pipeline_runs WHERE id=$1 FOR NO KEY UPDATE`, in.Run.ID).Scan(&id); err != nil {
					return err
				}
				err = db.NewHangarOutputRepository(prefix).ReleaseClaim(context.Background(), tx, output.ClaimRelease{ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: ref, RequestedAt: output.NewTimestamp(time.Now().UTC())})
				if err == nil {
					err = tx.Commit()
				}
				if err == nil {
					return fmt.Errorf("generic release discarded a durable Run input claim")
				}
			}
			return nil
		}),
	}
}

func (in RunInputAdmission) reclaimInputPayload() error {
	jdb := in.Source.Start.DB
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	run, found, err := factory.GetRunByID(in.Run.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("consumer Run missing")
	}
	var buildID int
	if err = jdb.Conn.QueryRow(`SELECT id FROM builds WHERE pipeline_run_id=$1`, in.Run.ID).Scan(&buildID); err != nil {
		return err
	}
	build, found, err := jdb.BuildFactory.Build(buildID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("consumer build missing")
	}
	if err = build.Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	definition, found, err := factory.Definition(run.ID())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("consumer definition missing")
	}
	start := RunOutputStart{DB: jdb, Creation: db.RunCreation{Run: run, Config: definition.Materialized}}
	if err = consumeRunScheduling(start); err != nil {
		return err
	}
	finished := finalizeRunResult(RunResultPublication{Start: start}, false)
	if finished.Err != nil {
		return finished.Err
	}
	if !finished.Completed {
		return fmt.Errorf("consumer Run did not complete")
	}
	in.Admission.ContractKey = "newer-input-run"
	if _, _, err = in.admitInput(false); err != nil {
		return err
	}
	removed, err := db.NewPipelineRunReclaimLifecycle(jdb.Conn).DestroyReclaimableRun(in.Run.ID)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("consumer payload was not reclaimed")
	}
	return nil
}

func (in RunInputAdmission) admitInput(rollback bool) (runs.Run, bool, error) {
	// Exercise the real admission document. Before implementation the missing
	// Inputs field is ignored, exposing admission without its required bindings.
	body, err := json.Marshal(map[string]any{"Inputs": in.Inputs})
	if err != nil {
		return runs.Run{}, false, err
	}
	admission := in.Admission
	if err = json.Unmarshal(body, &admission); err != nil {
		return runs.Run{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := in.Port.Begin(ctx)
	if err != nil {
		return runs.Run{}, false, err
	}
	defer tx.Rollback()
	run, replay, err := in.Port.AdmitVersionedRun(ctx, tx, admission, int64(hangarEpoch))
	if err != nil {
		return run, replay, err
	}
	if rollback {
		return run, replay, tx.Rollback()
	}
	return run, replay, tx.Commit()
}
