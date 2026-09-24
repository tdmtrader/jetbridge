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
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc"
	"golang.org/x/oauth2"
)

func RunInvocationAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("its versioned HTTP invocation encounters {string}", []string{"auth-server", "real-cluster"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseRunInvocationAPI(in, mode, rec, res)
	})}
}

func exerciseRunInvocationAPI(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) error {
	auth, _, err := configureRunInputAPI(in, "owner", rec, res)
	if err != nil {
		return err
	}
	login := func(user string) (string, error) {
		if _, err := auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", user, "-p", authPassword); err != nil {
			return "", err
		}
		token, err := auth.savedFlyToken()
		return token.Type + " " + token.Value, err
	}
	ownerToken, err := login("owner")
	if err != nil {
		return err
	}
	if strings.HasPrefix(mode, "shared client") {
		return exerciseSharedInvocationAPI(in, auth, mode == "shared client replay")
	}
	var header http.Header
	send := func(path, token string, body []byte) (int, []byte, error) {
		req, err := http.NewRequest(http.MethodPost, auth.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		response, err := auth.Client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		header = response.Header
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, data, err
	}
	archive, err := durableTarOfOneFile("manifest.json", "versioned HTTP invocation")
	if err != nil {
		return err
	}
	status, data, err := send("/api/v1/teams/output-start/pipelines/input-review/run-inputs/change", ownerToken, archive)
	if err != nil || status != http.StatusCreated {
		return fmt.Errorf("prepare HTTP input: status=%d error=%v", status, err)
	}
	var source atc.RunInputSource
	if err := json.Unmarshal(data, &source); err != nil {
		return err
	}
	request := map[string]any{"invocation_key": "brine-http-invocation", "inputs": map[string]atc.RunInputSource{"change": source}}
	token := ownerToken
	team, _, err := in.Source.Start.DB.TeamFactory.FindTeam("output-start")
	if err != nil {
		return err
	}
	want := http.StatusCreated
	switch mode {
	case "anonymous":
		token = ""
		want = http.StatusUnauthorized
	case "viewer":
		token, err = login("viewer")
		want = http.StatusForbidden
	case "cross-team":
		err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:someone-else"}}})
		want = http.StatusForbidden
	case "custom create role":
		err = team.UpdateProviderAuth(atc.TeamAuth{"member": {"users": {"local:owner"}}})
		auth.mu.Lock()
		auth.CustomRoles = map[string]string{atc.CreatePipelineRunV2: "owner"}
		auth.API, err = auth.apiHandler(auth.Verifier)
		auth.mu.Unlock()
		want = http.StatusForbidden
	case "operator hold":
		atc.EnablePipelineRunCreation = false
		want = http.StatusConflict
	case "database hold":
		_, err = in.Source.Start.DB.Conn.Exec(`UPDATE pipeline_run_activation SET admission_enabled=false WHERE singleton`)
		want = http.StatusConflict
	case "paused template":
		err = in.Template.Pause("brine")
		want = http.StatusConflict
	case "invalid key":
		request["invocation_key"] = "bad key"
		want = http.StatusBadRequest
	case "unknown field":
		request["unrecognized_intent"] = 1
		want = http.StatusBadRequest
	case "trailing JSON":
		want = http.StatusBadRequest
	case "foreign grant":
		err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner", "local:viewer"}}})
		token, err = login("viewer")
		want = http.StatusBadRequest
	}
	if err != nil {
		return err
	}
	var before int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&before); err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if mode == "trailing JSON" {
		body = append(body, []byte(" {}")...)
	}
	path := "/api/v2/teams/output-start/pipelines/input-review/runs"
	status, data, err = send(path, token, body)
	if err != nil || status != want {
		return fmt.Errorf("%s versioned invocation: HTTP %d, want %d; error=%v", mode, status, want, err)
	}
	var first atc.PipelineRun
	if want == http.StatusCreated {
		if json.Unmarshal(data, &first) != nil || first.ID <= 0 || first.Number != before+1 || first.ContractVersion != atc.RunContractV2 {
			return fmt.Errorf("versioned invocation returned no durable v2 identity")
		}
		if strings.Contains(string(data), source.Bearer) {
			return fmt.Errorf("Run response disclosed the input bearer")
		}
		// Requirement 20: a new Run says so and carries no replay header.
		if first.AdmissionOutcome != atc.RunAdmissionCreated || header.Get(atc.IdempotencyReplayedHeader) != "" {
			return fmt.Errorf("new Run signalled outcome %q with replay header %q", first.AdmissionOutcome, header.Get(atc.IdempotencyReplayedHeader))
		}
		if mode != "accepted" {
			if mode == "replay without bearer" {
				source.Bearer = ""
			}
			if mode == "changed input replay" {
				source.SourceID = strings.Repeat("0", 64)
			}
			if mode == "replay archived template" {
				if err := in.Template.Archive(); err != nil {
					return err
				}
			}
			request["inputs"] = map[string]atc.RunInputSource{"change": source}
			body, _ = json.Marshal(request)
			// The first response may have been lost. A new HTTP connection still
			// presents the same intent; the server owns the only replay record.
			auth.Client.CloseIdleConnections()
			status, data, err = send(path, token, body)
			replayWant := http.StatusOK
			if mode == "changed input replay" {
				replayWant = http.StatusConflict
			}
			if err != nil || status != replayWant {
				return fmt.Errorf("%s replay: HTTP %d, want %d; error=%v", mode, status, replayWant, err)
			}
			if replayWant == http.StatusOK {
				var replay atc.PipelineRun
				if json.Unmarshal(data, &replay) != nil || replay.ID != first.ID || replay.Number != first.Number {
					return fmt.Errorf("replay returned another Run")
				}
				if replay.AdmissionOutcome != atc.RunAdmissionReplayed || header.Get(atc.IdempotencyReplayedHeader) != "true" {
					return fmt.Errorf("replay signalled outcome %q with replay header %q", replay.AdmissionOutcome, header.Get(atc.IdempotencyReplayedHeader))
				}
			}
		}
	}
	var after int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&after); err != nil {
		return err
	}
	delta := 0
	if want == http.StatusCreated {
		delta = 1
	}
	if after != before+delta {
		return fmt.Errorf("HTTP admission created %d Runs, want %d", after-before, delta)
	}
	var leaked int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE position($1 in caller_document)>0`, source.Bearer).Scan(&leaked); err != nil {
		return err
	}
	if source.Bearer != "" && leaked != 0 {
		return fmt.Errorf("invocation persisted a bearer")
	}
	return nil
}

func exerciseSharedInvocationAPI(in RunInputAdmission, auth *AuthFixture, replay bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token, err := auth.savedFlyToken()
	if err != nil {
		return err
	}
	makeClient := func() (*reviewclient.Client, error) {
		transport := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.Value, TokenType: token.Type}))
		return reviewclient.New(auth.URL, transport)
	}
	client, err := makeClient()
	if err != nil {
		return err
	}
	type submissionClient interface {
		UploadInput(context.Context, string, string, string, io.Reader) (atc.RunInputSource, error)
		CreateRun(context.Context, string, string, atc.CreatePipelineRunV2Request) (atc.PipelineRun, error)
	}
	port, ok := any(client).(submissionClient)
	if !ok {
		return fmt.Errorf("shared client has no input upload and versioned admission operations")
	}
	archive, err := durableTarOfOneFile("manifest.json", "shared review client input")
	if err != nil {
		return err
	}
	source, err := port.UploadInput(ctx, "output-start", "input-review", "change", bytes.NewReader(archive))
	if err != nil || source.Validate() != nil || source.Bearer == "" {
		return fmt.Errorf("shared input upload: %v", err)
	}
	request := atc.CreatePipelineRunV2Request{InvocationKey: "shared-client", Inputs: map[string]atc.RunInputSource{"change": source}}
	first, err := port.CreateRun(ctx, "output-start", "input-review", request)
	if err != nil || first.ID <= 0 || first.ContractVersion != atc.RunContractV2 {
		return fmt.Errorf("shared Run admission: %v", err)
	}
	fresh, err := makeClient()
	if err != nil {
		return err
	}
	if replay {
		port = any(fresh).(submissionClient)
		request.Inputs["change"] = source.WithoutBearer()
		again, err := port.CreateRun(ctx, "output-start", "input-review", request)
		if err != nil || again.ID != first.ID || again.Number != first.Number {
			return fmt.Errorf("fresh client replay: %v", err)
		}
	}
	observed, err := fresh.Status(ctx, reviewclient.Handle{Team: "output-start", Template: "input-review", Number: first.Number})
	if err != nil || observed.ID != first.ID || observed.Status != atc.RunStatusRunning {
		return fmt.Errorf("fresh client cannot see the admitted Run: %v", err)
	}
	var count int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE template_pipeline_id=$1 AND run_id=$2`, in.Template.ID(), first.ID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("shared client did not retain exactly one invocation")
	}
	return nil
}
