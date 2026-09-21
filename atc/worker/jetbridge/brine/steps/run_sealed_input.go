package steps

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
)

func RunSealedInputDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("its input invocation receives a sealed source with {string}", []string{"real-cluster"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseRunSealedInput(in, mode, rec, res)
	})}
}

func exerciseRunSealedInput(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) error {
	if in.Err != nil || in.Source.Candidate == nil {
		return fmt.Errorf("source fixture failed: %v", in.Err)
	}
	now := time.Now().UTC()
	authority, err := runinput.NewAuthority(bytes.Repeat([]byte{0x51}, 32), func() time.Time { return now })
	if err != nil {
		return err
	}
	configured, ok := any(in.Port).(interface{ SetSealedInputAuthority(*runinput.Authority) })
	if !ok {
		return fmt.Errorf("Run admission has no sealed input grant authority")
	}
	configured.SetSealedInputAuthority(authority)
	if mode == "missing authority" {
		configured.SetSealedInputAuthority(nil)
	}
	subject := in.Admission.Principal.Claims["sub"].(string)
	audience := runinput.Audience{TeamID: in.Template.TeamID(), TemplateID: in.Template.ID(), PrincipalDigest: runinput.PrincipalDigest(subject), Input: "change", Epoch: int64(hangarEpoch)}
	ref := in.Source.Candidate.Record.Ref
	switch mode {
	case "wrong epoch":
		audience.Epoch++
	case "wrong team":
		audience.TeamID++
	case "wrong template":
		audience.TemplateID++
	case "wrong principal":
		audience.PrincipalDigest = runinput.PrincipalDigest("someone-else")
	case "wrong input":
		audience.Input = "other"
	case "unpublished generation":
		ref.Generation++
	}
	id, bearer, err := authority.Mint(audience, ref, 5*time.Minute)
	if err != nil {
		return err
	}
	in.Admission.ContractKey = "sealed-input"
	in.Inputs = map[string]map[string]any{"change": {"source_id": id, "bearer": bearer}}
	if mode == "expired grant" {
		now = now.Add(6 * time.Minute)
	}
	if mode == "tampered grant" {
		in.Inputs["change"]["bearer"] = bearer + "x"
	}
	if mode == "wrong source identity" {
		in.Inputs["change"]["source_id"] = "input-v1-" + strings.Repeat("0", 64)
	}
	conn := in.Source.Start.DB.Conn
	var before int
	if err = conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&before); err != nil {
		return err
	}
	run, replayed, admitErr := in.admitInput(mode == "rollback then expired")
	invalid := false
	switch mode {
	case "missing authority", "wrong epoch", "wrong team", "wrong template", "wrong principal", "wrong input", "unpublished generation", "expired grant", "tampered grant", "wrong source identity":
		invalid = true
	}
	expectedCount := before
	if invalid {
		if !errors.Is(admitErr, atc.ErrRunInputUnavailable) {
			return fmt.Errorf("sealed source was not refused opaquely for %s: %v", mode, admitErr)
		}
	} else {
		if admitErr != nil || replayed {
			return fmt.Errorf("first sealed admission failed: replay=%v err=%v", replayed, admitErr)
		}
		if mode == "rollback then expired" {
			now = now.Add(6 * time.Minute)
			_, _, err = in.admitInput(false)
			if !errors.Is(err, atc.ErrRunInputUnavailable) {
				return fmt.Errorf("uncommitted retry did not require a live grant: %v", err)
			}
		} else {
			expectedCount++
			if strings.Contains(mode, "replay") {
				switch mode {
				case "expired replay":
					now = now.Add(6 * time.Minute)
					delete(in.Inputs["change"], "bearer")
				case "missing bearer replay":
					delete(in.Inputs["change"], "bearer")
				case "changed bearer replay":
					in.Inputs["change"]["bearer"] = "invalid-but-irrelevant-after-commit"
				case "changed source identity replay":
					in.Inputs["change"]["source_id"] = "input-v1-" + strings.Repeat("0", 64)
				case "missing source identity replay":
					delete(in.Inputs["change"], "source_id")
				}
				again, replay, replayErr := in.admitInput(false)
				if mode == "changed source identity replay" || mode == "missing source identity replay" {
					if !errors.Is(replayErr, runs.ErrInvocationConflict) {
						return fmt.Errorf("changed sealed identity did not conflict: %v", replayErr)
					}
				} else if replayErr != nil || !replay || again.ID != run.ID {
					return fmt.Errorf("committed sealed replay did not return its original Run: replay=%v err=%v", replay, replayErr)
				}
			}
			var sourceID string
			var bound hangar.TreeRef
			if err = conn.QueryRow(`SELECT source_id,scope,digest,generation FROM pipeline_run_inputs WHERE run_id=$1 AND name='change'`, run.ID).Scan(&sourceID, &bound.Scope, &bound.Digest, &bound.Generation); err != nil {
				return err
			}
			if sourceID != id || bound != ref {
				return fmt.Errorf("sealed admission changed its authenticated source")
			}
			var retained string
			if err = conn.QueryRow(`SELECT to_jsonb(i)::text || to_jsonb(b)::text FROM pipeline_run_invocations i JOIN pipeline_run_inputs b ON b.run_id=i.run_id WHERE i.run_id=$1 AND b.name='change'`, run.ID).Scan(&retained); err != nil {
				return err
			}
			if strings.Contains(retained, bearer) || strings.Contains(retained, `"bearer"`) {
				return fmt.Errorf("sealed bearer reached retained Run history")
			}
			var active int
			if err = conn.QueryRow(`SELECT count(*) FROM pipeline_run_inputs i JOIN hangar_claims c ON c.claim_id=i.claim_id WHERE i.run_id=$1 AND c.released_at IS NULL`, run.ID).Scan(&active); err != nil {
				return err
			}
			if active != 1 {
				return fmt.Errorf("sealed admission has no exact active Run claim")
			}
		}
	}
	var after int
	if err = conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&after); err != nil {
		return err
	}
	if after != expectedCount {
		return fmt.Errorf("sealed admission allocated %d Runs, expected %d", after-before, expectedCount-before)
	}
	if mode == "task delivery" {
		in.Run = run
		return exerciseRunInputDelivery(in, "live", rec, res)
	}
	return nil
}
