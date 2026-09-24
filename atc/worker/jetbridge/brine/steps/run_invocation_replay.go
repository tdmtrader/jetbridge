package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

// This describes the missing core seam without substituting its behavior.
type versionedRunAdmitter interface {
	AdmitVersionedRun(context.Context, runs.Tx, runs.Admission, int64) (runs.Run, bool, error)
}

type InvocationReplay struct {
	DB            JetbridgeDB
	Team          db.Team
	Template      db.Pipeline
	Config        atc.Config
	Port          runs.Admitter
	Admission     runs.Admission
	Case          string
	First, Second runs.Run
	// Cause is the earlier Run a caused case links to; zero when none.
	Cause    int
	Replayed bool
	Err      error
}

func RunInvocationReplayDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, InvocationReplay]("a versioned invocation of a parameterized template", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (InvocationReplay, error) {
			previousGate := atc.EnablePipelineRunCreation
			atc.EnablePipelineRunCreation = true
			TrackDisposer(rec, "the invocation creation gate", func() error { atc.EnablePipelineRunCreation = previousGate; return nil })
			jdb, err := jetbridgeDBFrom(res)
			if err != nil {
				return InvocationReplay{}, err
			}
			in := InvocationReplay{DB: jdb}
			in.Team, err = jdb.TeamFactory.CreateTeam(atc.Team{Name: "invocation-team", Auth: atc.TeamAuth{"member": {"users": {"local:owner", "local:other-owner"}}}})
			if err != nil {
				return in, err
			}
			in.Config = atc.Config{Template: true, Params: []atc.ParamSchema{{Name: "target", Type: atc.ParamTypeString, Default: "original"}}, Jobs: atc.JobConfigs{{Name: "review", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "review", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true", Args: []string{"((target))"}}}}}}}}}
			in.Template, _, err = in.Team.SavePipeline(atc.PipelineRef{Name: "review"}, in.Config, 0, false)
			if err != nil {
				return in, err
			}
			if err = openActivationEpoch(jdb); err != nil {
				return in, err
			}
			if _, err = jdb.Conn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, int64(hangarEpoch)); err != nil {
				return in, err
			}
			display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
			if err != nil {
				return in, err
			}
			in.Port = runs.NewAdmitter(jdb.Conn, db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory), jdb.TeamFactory, display, nil)
			in.Admission = runs.Admission{Template: runs.TemplateRef{Team: in.Team.Name(), Pipeline: in.Template.PipelineRef()}, Principal: invocationPrincipal("owner"), ContractKey: "review-request.1~a"}
			return in, nil
		}),
		brine.DefineMap[InvocationReplay, InvocationReplay]("its caller submits with {string}", func(in InvocationReplay, p brine.Params, _ *brine.Recorder) (InvocationReplay, error) {
			in.Case, _ = p.GetString(0)
			if invalidInvocationCase(in.Case) {
				switch in.Case {
				case "an empty key":
					in.Admission.ContractKey = ""
				case "an oversized key":
					in.Admission.ContractKey = strings.Repeat("x", 129)
				case "a key containing spaces":
					in.Admission.ContractKey = "private key"
				case "a non-ASCII key":
					in.Admission.ContractKey = "invocation-é"
				case "no stable principal":
					delete(in.Admission.Principal.Claims, "sub")
				case "a stronger v2 capability", "a weakened v2 capability":
					display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
					if err != nil {
						return in, err
					}
					role := accessor.OwnerRole
					if in.Case == "a weakened v2 capability" {
						role = accessor.ViewerRole
					}
					in.Port = runs.NewAdmitter(in.DB.Conn, db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory), in.DB.TeamFactory, display, map[string]string{atc.CreatePipelineRunV2: role})
				case "disabled activation":
					if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_activation SET admission_enabled=false WHERE singleton`); err != nil {
						return in, err
					}
				case "an unavailable cause":
					// Absent, another team's, or later: one refusal, no oracle.
					missing := 1 << 30
					in.Admission.CausedByRun = &missing
				case "an invalid correlation":
					in.Admission.Correlation = "batch/1"
				}
				in.First, in.Replayed, in.Err = in.admit(false)
				return in, nil
			}
			if in.Case == "concurrent requests" {
				in.DB.Conn.SetMaxOpenConns(4)
				var admitted [2]runs.Run
				var replay [2]bool
				var errs [2]error
				var group sync.WaitGroup
				start := make(chan struct{})
				for i := range admitted {
					group.Add(1)
					go func(i int) { defer group.Done(); <-start; admitted[i], replay[i], errs[i] = in.admit(false) }(i)
				}
				close(start)
				group.Wait()
				for _, err := range errs {
					if err != nil {
						in.Err = err
						return in, nil
					}
				}
				if replay[0] == replay[1] {
					return in, fmt.Errorf("concurrent admission did not identify one creation and one replay")
				}
				in.First, in.Second, in.Replayed = admitted[0], admitted[1], true
				return in, nil
			}
			if in.Case == "a maximum length key" {
				in.Admission.ContractKey = strings.Repeat("a", 128)
			}
			switch in.Case {
			case "a correlated replay", "a changed correlation":
				in.Admission.Correlation = "review.batch-1~a"
			case "a caused replay", "a changed cause":
				intended := in.Admission
				in.Admission.ContractKey = "predecessor"
				predecessor, _, err := in.admit(false)
				if err != nil {
					return in, err
				}
				in.Admission = intended
				in.Cause = predecessor.ID
				in.Admission.CausedByRun = &in.Cause
				in.Admission.Correlation = "review.batch-1~a"
			}
			if in.Case == "a failed first callback" {
				in.Admission.BeforeCommit = func(tx runs.Tx, run runs.Run) error {
					var present bool
					if err := tx.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM pipeline_run_invocations WHERE run_id=$1)`, run.ID).Scan(&present); err != nil {
						return err
					}
					if !present {
						return fmt.Errorf("callback cannot see atomic invocation record")
					}
					return errors.New("consumer rollback")
				}
			}
			in.First, _, in.Err = in.admit(in.Case == "a rolled back first transaction")
			if in.Case == "a failed first callback" {
				if in.Err == nil || in.Err.Error() != "consumer rollback" {
					return in, fmt.Errorf("expected consumer rollback, got %v", in.Err)
				}
				in.Err = nil
				in.Admission.BeforeCommit = nil
			}
			if in.Err != nil {
				return in, nil
			}
			switch in.Case {
			case "an unchanged replay", "a rolled back first transaction", "a failed first callback", "a maximum length key", "a correlated replay", "a caused replay":
			case "a changed correlation":
				in.Admission.Correlation = "review.batch-2"
			case "a changed cause":
				moved := in.First.ID
				in.Admission.CausedByRun = &moved
			case "an explicit null":
				in.Admission.Params = atc.RunParams{"target": nil}
			case "a replay callback":
				in.Admission.BeforeCommit = func(runs.Tx, runs.Run) error { return errors.New("replay reran consumer creation callback") }
			case "retained record mutation":
				for _, query := range []string{`DELETE FROM pipeline_run_invocations WHERE run_id=$1`, `UPDATE pipeline_run_invocations SET caller_digest=repeat('0',64) WHERE run_id=$1`} {
					if _, err := in.DB.Conn.Exec(query, in.First.ID); err == nil {
						return in, fmt.Errorf("retained invocation changed before its Run was purged")
					}
				}
			case "a removed template parameter":
				in.Config.Params = nil
				in.Config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Run.Args = nil
				var err error
				in.Template, _, err = in.Team.SavePipeline(in.Template.PipelineRef(), in.Config, in.Template.ConfigVersion(), false)
				if err != nil {
					return in, err
				}
			case "a changed parameter":
				in.Admission.Params = atc.RunParams{"target": "changed"}
			case "an explicitly supplied default":
				in.Admission.Params = atc.RunParams{"target": "original"}
			case "a changed template default":
				in.Config.Params[0].Default = "changed"
				var err error
				in.Template, _, err = in.Team.SavePipeline(in.Template.PipelineRef(), in.Config, in.Template.ConfigVersion(), false)
				if err != nil {
					return in, err
				}
			case "a paused template":
				if err := in.Template.Pause("brine invocation replay"); err != nil {
					return in, err
				}
			case "a different principal":
				in.Admission.Principal = invocationPrincipal("other-owner")
			case "a different key":
				in.Admission.ContractKey = "review-request.2"
			case "revoked team membership":
				if err := in.Team.UpdateProviderAuth(atc.TeamAuth{"member": {"users": {"local:other-owner"}}}); err != nil {
					return in, err
				}
			default:
				return in, fmt.Errorf("unknown invocation case %q", in.Case)
			}
			in.Second, in.Replayed, in.Err = in.admit(false)
			return in, nil
		}),
		CheckThat[InvocationReplay]("the scoped invocation outcome is correct", func(in InvocationReplay) error {
			var count, number, records int
			if err := in.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1),last_run_number FROM pipelines WHERE id=$1`, in.Template.ID()).Scan(&count, &number); err != nil {
				return err
			}
			if err := in.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&records); err != nil {
				return err
			}
			if records != count {
				return fmt.Errorf("Run and invocation record counts disagree: %d / %d", count, records)
			}
			if invalidInvocationCase(in.Case) {
				if in.Err == nil || count != 0 || number != 0 {
					return fmt.Errorf("invalid invocation admitted a Run or allocated a number: error=%v count=%d number=%d", in.Err, count, number)
				}
				want := "invalid invocation key"
				if in.Case == "a weakened v2 capability" {
					var refusal runs.CustomRolesInvalidError
					if !errors.As(in.Err, &refusal) {
						return fmt.Errorf("weakened v2 capability was not refused: %v", in.Err)
					}
					return nil
				}
				if in.Case == "disabled activation" {
					if !errors.Is(in.Err, atc.ErrRunResultsUnavailable) {
						return fmt.Errorf("activation hold was bypassed: %v", in.Err)
					}
					return nil
				}
				if in.Case == "an unavailable cause" {
					if !errors.Is(in.Err, runs.ErrRunCauseUnavailable) {
						return fmt.Errorf("unavailable cause was not refused: %v", in.Err)
					}
					return nil
				}
				if in.Case == "an invalid correlation" {
					if !errors.Is(in.Err, runs.ErrInvalidCorrelation) {
						return fmt.Errorf("invalid correlation was not refused: %v", in.Err)
					}
					return nil
				}
				if in.Case == "no stable principal" || in.Case == "a stronger v2 capability" {
					if !errors.Is(in.Err, runs.ErrUnauthorized) {
						return fmt.Errorf("expected unauthorized principal refusal, got %v", in.Err)
					}
					return nil
				}
				if !strings.Contains(strings.ToLower(in.Err.Error()), want) {
					return fmt.Errorf("expected %s, got %v", want, in.Err)
				}
				return nil
			}
			// A caused case admitted its predecessor first.
			if in.Cause != 0 {
				count, number = count-1, number-1
			}
			if in.Case == "a changed parameter" || in.Case == "an explicitly supplied default" || in.Case == "an explicit null" || in.Case == "revoked team membership" || in.Case == "a changed correlation" || in.Case == "a changed cause" {
				want := "conflict"
				refused := in.Err != nil && strings.Contains(strings.ToLower(in.Err.Error()), want)
				if in.Case == "revoked team membership" {
					want = "unauthorized"
					refused = errors.Is(in.Err, runs.ErrUnauthorized)
				}
				if !refused || count != 1 || number != 1 {
					return fmt.Errorf("expected %s without a second Run, got %v (count %d, number %d)", want, in.Err, count, number)
				}
				return nil
			}
			if in.Err != nil {
				return in.Err
			}
			newRun := in.Case == "a different key" || in.Case == "a different principal"
			if newRun {
				if in.Replayed || in.First.ID == in.Second.ID || count != 2 || number != 2 {
					return fmt.Errorf("separate invocation scope did not create a distinct Run")
				}
			} else if in.Case == "a rolled back first transaction" || in.Case == "a failed first callback" {
				if in.Replayed || count != 1 || number != 1 {
					return fmt.Errorf("rollback left a replay record or allocated number")
				}
			} else if !in.Replayed || in.First.ID != in.Second.ID || count != 1 || number != 1 {
				return fmt.Errorf("replay did not preserve the original Run and number")
			}
			var target, version, correlation string
			var cause int
			if err := in.DB.Conn.QueryRow(`SELECT params->>'target',run_contract_version,coalesce(correlation,''),coalesce(caused_by_run,0) FROM pipeline_runs WHERE id=$1`, in.Second.ID).Scan(&target, &version, &correlation, &cause); err != nil {
				return err
			}
			if target != "original" || version != "v2" {
				return fmt.Errorf("admission lost its original defaults or v2 contract")
			}
			if correlation != in.Admission.Correlation || cause != in.Cause {
				return fmt.Errorf("Run retained correlation %q and cause %d, want %q and %d", correlation, cause, in.Admission.Correlation, in.Cause)
			}
			return nil
		}),
	}
}

func (in InvocationReplay) admit(rollback bool) (runs.Run, bool, error) {
	port, ok := in.Port.(versionedRunAdmitter)
	if !ok {
		return runs.Run{}, false, fmt.Errorf("missing versioned Run admission with scoped replay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := in.Port.Begin(ctx)
	if err != nil {
		return runs.Run{}, false, err
	}
	defer tx.Rollback()
	run, replay, err := port.AdmitVersionedRun(ctx, tx, in.Admission, int64(hangarEpoch))
	if err != nil {
		return run, replay, err
	}
	if rollback {
		return run, replay, tx.Rollback()
	}
	return run, replay, tx.Commit()
}

func invalidInvocationCase(value string) bool {
	switch value {
	case "an empty key", "an oversized key", "a key containing spaces", "a non-ASCII key", "no stable principal", "a stronger v2 capability", "a weakened v2 capability", "disabled activation", "an unavailable cause", "an invalid correlation":
		return true
	}
	return false
}

func invocationPrincipal(name string) runs.Principal {
	return runs.Principal{Claims: map[string]any{"sub": "local:" + name, "name": name, "federated_claims": map[string]any{"connector_id": "local", "user_id": name}}}
}
