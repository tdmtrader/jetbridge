package steps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

type runInputIntakePort interface {
	SetInputUploadConfig(runs.InputUploadConfig)
	UploadInput(context.Context, runs.TemplateRef, runs.Principal, string, int64, io.Reader) (atc.RunInputSource, error)
}

func RunInputIntakeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[HangarDaemon, HangarDaemon]("its authenticated input upload encounters {string}", []string{"jetbridge-db"}, func(in HangarDaemon, p brine.Params, _ *brine.Recorder, res brine.Resources) (HangarDaemon, error) {
		mode, _ := p.GetString(0)
		jdb, err := jetbridgeDBFrom(res)
		if err != nil {
			return in, err
		}
		return in, exerciseRunInputIntake(in, jdb, mode)
	})}
}

func exerciseRunInputIntake(in HangarDaemon, jdb JetbridgeDB, mode string) error {
	previousGate := atc.PipelineRunActivationEpoch
	atc.PipelineRunActivationEpoch = int64(hangarEpoch)
	defer func() { atc.PipelineRunActivationEpoch = previousGate }()
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	if err != nil {
		return err
	}
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	var customRoles map[string]string
	if mode == "custom role revoked during stream" {
		customRoles = map[string]string{atc.UploadPipelineRunInput: "owner"}
	}
	admitter := runs.NewAdmitter(jdb.Conn, factory, jdb.TeamFactory, display, customRoles)
	admitter.SetOutputEpoch(int64(hangarEpoch))
	intake, ok := any(admitter).(runInputIntakePort)
	if !ok {
		return fmt.Errorf("Run admission has no authenticated input intake")
	}
	if err := openActivationEpoch(jdb); err != nil {
		return err
	}
	if _, err := db.ReconcilePipelineRunActivation(context.Background(), jdb.Conn, int64(hangarEpoch)); err != nil {
		return err
	}
	team, err := jdb.TeamFactory.CreateTeam(atc.Team{Name: "input-intake"})
	if err != nil {
		return err
	}
	role := "member"
	if mode == "custom role revoked during stream" {
		role = "owner"
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{role: {"users": {"local:owner"}}}); err != nil {
		return err
	}
	config := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "review", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "review", TaskID: freshUUID(), RunInputs: []atc.RunInput{{Name: "change", Input: "source"}}, Config: &atc.TaskConfig{Platform: "linux", Inputs: []atc.TaskInputConfig{{Name: "source"}}, Run: atc.TaskRunConfig{Path: "true"}}}}}}}}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "review"}, config, 0, false)
	if err != nil {
		return err
	}
	authority, err := runinput.NewAuthority(bytes.Repeat([]byte{0x53}, 32), time.Now)
	if err != nil {
		return err
	}
	admitter.SetSealedInputAuthority(authority)
	verifier, err := inputPublicationVerifier(in)
	if err != nil {
		return err
	}
	uploadConfig := runs.InputUploadConfig{Source: func(ctx context.Context, epoch int64) (runs.InputUploadNode, error) {
		uid := executioncontrol.NodeUID(in.NodeUID)
		if mode == "wrong node" {
			uid = executioncontrol.NodeUID(freshUUID())
		}
		return runs.InputUploadNode{UID: uid, Publisher: jetbridge.NewOutputControlClient(in.Output.URL, in.HTTP, in.Minter, executioncontrol.ActivationEpoch(epoch)), Verifier: verifier}, nil
	}}
	expires := mode == "unused upload expires" || mode == "Run claim survives expiry" || mode == "expired grant" || mode == "replay after expiry" || mode == "expiry while held"
	if expires {
		uploadConfig.ClaimTTL = 3 * time.Second
	}
	intake.SetInputUploadConfig(uploadConfig)
	ref := runs.TemplateRef{Team: team.Name(), Pipeline: template.PipelineRef()}
	principal := invocationPrincipal("owner")
	name, epoch := "change", int64(hangarEpoch)
	archive, err := durableTarOfOneFile("manifest.json", "local input review")
	if err != nil {
		return err
	}
	refuseBeforeRead := true
	switch mode {
	case "foreign principal":
		principal = invocationPrincipal("someone-else")
	case "unknown team":
		ref.Team = "missing-team"
	case "unknown input":
		name = "undeclared"
	case "paused template":
		if err := template.Pause("owner"); err != nil {
			return err
		}
	case "held activation":
		if _, err := db.ReconcilePipelineRunActivation(context.Background(), jdb.Conn, 0); err != nil {
			return err
		}
	case "wrong epoch":
		epoch++
	case "missing authority":
		admitter.SetSealedInputAuthority(nil)
	case "unconfigured upload":
		intake.SetInputUploadConfig(runs.InputUploadConfig{})
	default:
		refuseBeforeRead = false
	}
	if mode == "malformed archive" {
		archive = []byte("not a tar")
	}
	if mode == "revoked during stream" || mode == "custom role revoked during stream" {
		ctx, cancel := context.WithTimeout(in.Ctx, 10*time.Second)
		defer cancel()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		finished := make(chan error, 1)
		go func() { _, err := intake.UploadInput(ctx, ref, principal, name, epoch, reader); finished <- err }()
		// The real HTTP transport has started reading only after initial
		// authorization. Keep the actual tar incomplete while revoking access.
		if _, err := writer.Write(archive[:512]); err != nil {
			return err
		}
		after := atc.TeamAuth{}
		if mode == "custom role revoked during stream" {
			after = atc.TeamAuth{"member": {"users": {"local:owner"}}}
		}
		if err := team.UpdateProviderAuth(after); err != nil {
			return err
		}
		if _, err := writer.Write(archive[512:]); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		select {
		case err := <-finished:
			if err != runs.ErrUnauthorized {
				return fmt.Errorf("upload did not reauthorize after its stream: %v", err)
			}
		case <-ctx.Done():
			return fmt.Errorf("revoked upload did not finish")
		}
		return assertInputObjectCount(in, 0)
	}
	body := bytes.NewReader(archive)
	source, uploadErr := intake.UploadInput(in.Ctx, ref, principal, name, epoch, body)
	if refuseBeforeRead || mode == "malformed archive" || mode == "wrong node" {
		if uploadErr == nil {
			return fmt.Errorf("input intake accepted %s", mode)
		}
		if refuseBeforeRead && body.Len() != len(archive) {
			return fmt.Errorf("input intake read bytes before refusing %s", mode)
		}
		return assertInputObjectCount(in, 0)
	}
	if uploadErr != nil || source.SourceID == "" || source.Bearer == "" || source.Validate() != nil {
		return fmt.Errorf("input intake did not return a sealed source: %v", uploadErr)
	}
	audience := runinput.Audience{TeamID: team.ID(), TemplateID: template.ID(), PrincipalDigest: runinput.PrincipalDigest(principal.Claims["sub"].(string)), Input: name, Epoch: epoch}
	tree, err := authority.Verify(source.SourceID, source.Bearer, audience)
	if err != nil {
		return fmt.Errorf("upload did not authorize the exact input audience: %w", err)
	}
	var retained string
	var deadline time.Time
	if err := jdb.Conn.QueryRow(`SELECT row_to_json(u)::text, expires_at FROM pipeline_run_input_uploads u WHERE template_pipeline_id=$1`, template.ID()).Scan(&retained, &deadline); err != nil {
		return err
	}
	if strings.Contains(retained, source.Bearer) || strings.Contains(retained, "bearer") {
		return fmt.Errorf("input upload retained its bearer")
	}
	var claims struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	encoded := strings.Split(source.Bearer, ".")
	payload, err := base64.RawURLEncoding.DecodeString(encoded[0])
	if err != nil || json.Unmarshal(payload, &claims) != nil || claims.ExpiresAt <= 0 || time.Unix(claims.ExpiresAt, 0).After(deadline) {
		return fmt.Errorf("input grant outlives its temporary claim")
	}
	if mode == "stable source" {
		again, err := intake.UploadInput(in.Ctx, ref, principal, name, epoch, bytes.NewReader(archive))
		if err != nil || again.SourceID != source.SourceID {
			return fmt.Errorf("identical upload changed source identity: %v", err)
		}
		return assertInputObjectCount(in, 1)
	}
	admission := runs.Admission{Template: ref, Principal: principal, ContractKey: "uploaded-change", Inputs: map[string]atc.RunInputSource{name: source}}
	admit := func() (runs.Run, bool, error) {
		tx, err := admitter.Begin(in.Ctx)
		if err != nil {
			return runs.Run{}, false, err
		}
		defer tx.Rollback()
		run, replayed, err := admitter.AdmitVersionedRun(in.Ctx, tx, admission, epoch)
		if err != nil {
			return run, replayed, err
		}
		return run, replayed, db.HangarCommitError(tx.Commit())
	}
	var run runs.Run
	if mode != "unused upload expires" && mode != "expired grant" {
		run, _, err = admit()
		if err != nil {
			return fmt.Errorf("admit uploaded input: %w", err)
		}
	}
	if expires {
		if mode == "expiry while held" {
			if _, err := db.ReconcilePipelineRunActivation(context.Background(), jdb.Conn, 0); err != nil {
				return err
			}
		}
		<-time.After(time.Until(deadline) + 100*time.Millisecond)
		if err := gc.NewPipelineRunReclaimer(db.NewPipelineRunReclaimLifecycle(jdb.Conn), time.Now, 20).Run(in.Ctx); err != nil {
			return fmt.Errorf("expire upload claims: %w", err)
		}
		var uploads, active int
		if err := jdb.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_input_uploads WHERE template_pipeline_id=$1), (SELECT count(*) FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id WHERE l.scope=$2 AND l.digest=$3 AND l.generation=$4 AND c.released_at IS NULL)`, template.ID(), string(tree.Scope), string(tree.Digest), tree.Generation).Scan(&uploads, &active); err != nil {
			return err
		}
		want := 0
		if run.ID != 0 {
			want = 1
		}
		if uploads != 0 || active != want {
			return fmt.Errorf("upload expiry retained %d upload rows and %d active claims, want 0 and %d", uploads, active, want)
		}
		if mode == "expired grant" {
			if _, _, err := admit(); err == nil {
				return fmt.Errorf("expired upload grant admitted a Run")
			}
		}
		if mode == "replay after expiry" {
			admission.Inputs[name] = source.WithoutBearer()
			again, replayed, err := admit()
			if err != nil || !replayed || again.ID != run.ID {
				return fmt.Errorf("admitted Run did not replay after upload expiry: %v", err)
			}
		}
	}
	return assertInputObjectCount(in, 1)
}
