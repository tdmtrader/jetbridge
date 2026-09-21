package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelinerunserver"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

func RunInputAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("its public input upload is requested by {string}", []string{"auth-server", "real-cluster"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		caller, _ := p.GetString(0)
		return in, exerciseRunInputAPI(in, caller, rec, res)
	})}
}

func configureRunInputAPI(in RunInputAdmission, caller string, rec *brine.Recorder, res brine.Resources) (*AuthFixture, *runinput.Authority, error) {
	if in.Err != nil {
		return nil, nil, in.Err
	}
	auth, err := authServer(res)
	if err != nil {
		return nil, nil, err
	}
	team, found, err := in.Source.Start.DB.TeamFactory.FindTeam("output-start")
	if err != nil || !found {
		return nil, nil, fmt.Errorf("upload team: %v", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner"}}, "viewer": {"users": {"local:viewer"}}}); err != nil {
		return nil, nil, err
	}
	if err := in.Template.Expose(); err != nil {
		return nil, nil, err
	}
	authority, err := runinput.NewAuthority(bytes.Repeat([]byte{0x56}, 32), time.Now)
	if err != nil {
		return nil, nil, err
	}
	in.Port.SetSealedInputAuthority(authority)
	if caller == "missing authority" {
		in.Port.SetSealedInputAuthority(nil)
	}
	daemon := in.Source.Start.Daemon
	verifier, err := inputPublicationVerifier(daemon)
	if err != nil {
		return nil, nil, err
	}
	outputSource, _, _, err := configureRunReadPlane(in.Source, rec, res)
	if err != nil {
		return nil, nil, err
	}
	in.Port.SetInputUploadConfig(runs.InputUploadConfig{Source: func(ctx context.Context, epoch int64) (runs.InputUploadNode, error) {
		client, uid, err := outputSource.ForInputUpload(ctx, executioncontrol.ActivationEpoch(epoch))
		return runs.InputUploadNode{UID: uid, Publisher: client, Verifier: verifier}, err
	}})
	oldEnabled := atc.EnablePipelineRunCreation
	atc.EnablePipelineRunCreation = caller != "held"
	TrackDisposer(rec, "the pipeline-run creation setting", func() error { atc.EnablePipelineRunCreation = oldEnabled; return nil })
	auth.mu.Lock()
	auth.RunServices = pipelinerunserver.Services{Admitter: in.Port, Epoch: int64(hangarEpoch)}
	auth.API, err = auth.apiHandler(auth.Verifier)
	auth.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return auth, authority, nil
}

func exerciseRunInputAPI(in RunInputAdmission, caller string, rec *brine.Recorder, res brine.Resources) error {
	auth, authority, err := configureRunInputAPI(in, caller, rec, res)
	if err != nil {
		return err
	}
	team, _, err := in.Source.Start.DB.TeamFactory.FindTeam("output-start")
	if err != nil {
		return err
	}
	if caller == "cross-team" {
		if err := team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:someone-else"}}}); err != nil {
			return err
		}
	}
	user := "owner"
	if caller == "viewer" {
		user = "viewer"
	}
	archive, err := durableTarOfOneFile("manifest.json", "public input upload")
	if err != nil {
		return err
	}
	if caller == "malformed archive" {
		archive = []byte("not a tar")
	}
	name := "change"
	if caller == "unknown input" {
		name = "undeclared"
	}
	req, err := http.NewRequest(http.MethodPost, auth.URL+"/api/v1/teams/output-start/pipelines/input-review/run-inputs/"+name, bytes.NewReader(archive))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	if caller != "anonymous" {
		if _, err := auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", user, "-p", authPassword); err != nil {
			return err
		}
		token, err := auth.savedFlyToken()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", token.Type+" "+token.Value)
	}
	response, err := auth.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil {
		return err
	}
	want := http.StatusCreated
	switch caller {
	case "anonymous":
		want = http.StatusUnauthorized
	case "viewer", "cross-team":
		want = http.StatusForbidden
	case "held":
		want = http.StatusConflict
	case "missing authority":
		want = http.StatusServiceUnavailable
	case "malformed archive", "unknown input":
		want = http.StatusBadRequest
	}
	if response.StatusCode != want {
		return fmt.Errorf("%s input upload returned HTTP %d, want %d", caller, response.StatusCode, want)
	}
	if want != http.StatusCreated {
		return nil
	}
	if response.Header.Get("Cache-Control") != "private, no-store" {
		return fmt.Errorf("input grant response is cacheable")
	}
	var source atc.RunInputSource
	if json.Unmarshal(body, &source) != nil || source.Validate() != nil || source.Bearer == "" {
		return fmt.Errorf("upload response has no typed sealed source")
	}
	claims, err := auth.Verifier.Verify(req)
	if err != nil {
		return err
	}
	_, err = authority.Verify(source.SourceID, source.Bearer, runinput.Audience{TeamID: team.ID(), TemplateID: in.Template.ID(), PrincipalDigest: runinput.PrincipalDigest(claims["sub"].(string)), Input: name, Epoch: int64(hangarEpoch)})
	if err != nil {
		return fmt.Errorf("HTTP grant belongs to another caller: %w", err)
	}
	if strings.Contains(string(auth.APILogger.Buffer().Contents()), source.Bearer) {
		return fmt.Errorf("API log contains the grant bearer")
	}
	return nil
}
