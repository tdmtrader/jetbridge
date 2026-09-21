package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/rc"
)

type RunCancellationAPI struct {
	Start  RunOutputStart
	Auth   *AuthFixture
	Token  rc.TargetToken
	Status int
	Body   []byte
}

func RunCancellationAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunCancellationAPI, RunCancellationAPI]("the client archives its Run template", func(in RunCancellationAPI, _ brine.Params, _ *brine.Recorder) (RunCancellationAPI, error) {
			team, found, err := in.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing team")
			}
			base, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing base")
			}
			return in, base.Archive()
		}),
		brine.DefineCheck[RunCancellationAPI]("its Run cancellation control is {string}", func(in RunCancellationAPI, p brine.Params, _ *brine.Recorder) error {
			if in.Status != http.StatusOK {
				return fmt.Errorf("detail returned HTTP %d", in.Status)
			}
			want, _ := p.GetString(0)
			var response struct {
				CanCancel bool `json:"can_cancel"`
			}
			if err := json.Unmarshal(in.Body, &response); err != nil {
				return err
			}
			if response.CanCancel != (want == "available") {
				return fmt.Errorf("cancel availability did not follow authorization")
			}
			return nil
		}),
		brine.DefineMap[RunCancellationAPI, RunCancellationAPI]("the client exposes the template and reads its Run anonymously", func(in RunCancellationAPI, _ brine.Params, _ *brine.Recorder) (RunCancellationAPI, error) {
			team, found, err := in.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing team")
			}
			base, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
			if err != nil {
				return in, err
			}
			if !found {
				return in, fmt.Errorf("missing base")
			}
			if err := base.Expose(); err != nil {
				return in, err
			}
			in.Token = rc.TargetToken{}
			return in.request(http.MethodGet, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d", in.Start.Creation.Run.Number()), nil)
		}),
		CheckThat[RunCancellationAPI]("the public Run detail omits every cancellation fact", func(in RunCancellationAPI) error {
			if in.Status != http.StatusOK {
				return fmt.Errorf("public detail returned HTTP %d", in.Status)
			}
			var response map[string]any
			if err := json.Unmarshal(in.Body, &response); err != nil {
				return err
			}
			if _, found := response["cancellation"]; found {
				return fmt.Errorf("public detail disclosed cancellation")
			}
			if _, found := response["can_cancel"]; found {
				return fmt.Errorf("public detail disclosed cancellation control")
			}
			if bytes.Contains(in.Body, []byte("first private reason")) {
				return fmt.Errorf("public detail disclosed reason")
			}
			return nil
		}),
		brine.DefineMap[RunCancellationAPI, RunCancellationAPI]("it posts cancellation with private reason text also in the query", func(in RunCancellationAPI, _ brine.Params, _ *brine.Recorder) (RunCancellationAPI, error) {
			body, _ := cancellationRequestBody("valid")
			return in.request(http.MethodPost, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d/cancel?reason=first+private+reason", in.Start.Creation.Run.Number()), body)
		}),
		CheckThat[RunCancellationAPI]("cancellation audit records omit private reason text", func(in RunCancellationAPI) error {
			found := false
			for _, entry := range in.Auth.APILogger.Logs() {
				body, err := json.Marshal(entry)
				if err != nil {
					return err
				}
				if bytes.Contains(body, []byte("first private reason")) {
					return fmt.Errorf("API audit leaked cancellation reason")
				}
				if strings.HasSuffix(entry.Message, "run-cancellation") && entry.Data["outcome"] == "accepted" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("no durable cancellation outcome was audited")
			}
			return nil
		}),

		brine.DefineMapUsing[brine.Empty, RunCancellationAPI]("a Run cancellation API client as {string}", []string{"auth-server", "jetbridge-db"}, func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunCancellationAPI, error) {
			auth, err := authServer(res)
			if err != nil {
				return RunCancellationAPI{}, err
			}
			out := RunCancellationAPI{Auth: auth}
			out.Start, err = runOutputFixture(rec, res, "current")
			if err != nil {
				return out, err
			}
			if out.Start.Err != nil {
				return out, out.Start.Err
			}
			team, found, err := out.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil {
				return out, err
			}
			if !found {
				return out, fmt.Errorf("missing Run team")
			}
			caller, _ := p.GetString(0)
			owner := "local:owner"
			if caller == "cross-team" {
				owner = "local:somebody-else"
			}
			if err := team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {owner}}, "viewer": {"users": {"local:viewer"}}}); err != nil {
				return out, err
			}
			if caller != "anonymous" {
				user := caller
				if caller == "cross-team" {
					user = "owner"
				}
				_, err = out.Auth.fly("login", "-c", out.Auth.URL, "-n", "auth-team", "-u", user, "-p", authPassword)
				if err != nil {
					return out, err
				}
				out.Token, err = out.Auth.savedFlyToken()
			}
			return out, err
		}),
		brine.DefineMap[RunCancellationAPI, RunCancellationAPI]("it posts a {string} cancellation request for the {string} Run", func(in RunCancellationAPI, p brine.Params, _ *brine.Recorder) (RunCancellationAPI, error) {
			kind, _ := p.GetString(0)
			target, _ := p.GetString(1)
			body, err := cancellationRequestBody(kind)
			if err != nil {
				return in, err
			}
			number := in.Start.Creation.Run.Number()
			if target == "unknown" {
				number += 100000
			}
			return in.request(http.MethodPost, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d/cancel", number), body)
		}),
		brine.DefineCheck[RunCancellationAPI]("the cancellation API reports {string} with a running Run", func(in RunCancellationAPI, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if in.Status != http.StatusOK {
				return fmt.Errorf("cancellation API returned HTTP %d, want 200", in.Status)
			}
			var response struct {
				Outcome string `json:"outcome"`
				Run     struct {
					Status string `json:"status"`
				} `json:"run"`
			}
			if err := json.Unmarshal(in.Body, &response); err != nil {
				return err
			}
			if response.Outcome != want || response.Run.Status != "running" {
				return fmt.Errorf("cancellation did not return its durable decision and running status")
			}
			return nil
		}),
		brine.DefineCheck[RunCancellationAPI]("its cancellation request is denied with HTTP {int}", func(in RunCancellationAPI, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetInt(0)
			if in.Status != want {
				return fmt.Errorf("cancellation returned HTTP %d, want %d", in.Status, want)
			}
			return nil
		}),
		CheckThat[RunCancellationAPI]("its first cancellation reason and requester remain authoritative", func(in RunCancellationAPI) error {
			var by, reason string
			err := in.Start.DB.Conn.QueryRow(`SELECT cancel_requested_by,cancel_reason FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&by, &reason)
			if err != nil {
				return err
			}
			if by == "" || reason != "first private reason" {
				return fmt.Errorf("cancellation replay replaced the first request")
			}
			return nil
		}),
		CheckThat[RunCancellationAPI]("the rejected API request leaves no cancellation facts", func(in RunCancellationAPI) error { return checkNoCancellation(in.Start) }),
		brine.DefineMap[RunCancellationAPI, RunCancellationAPI]("it reads the Run detail through the authorized API", func(in RunCancellationAPI, _ brine.Params, _ *brine.Recorder) (RunCancellationAPI, error) {
			return in.request(http.MethodGet, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d", in.Start.Creation.Run.Number()), nil)
		}),
		CheckThat[RunCancellationAPI]("its Run detail describes the accepted cancellation while still running", func(in RunCancellationAPI) error {
			if in.Status != http.StatusOK {
				return fmt.Errorf("Run detail returned HTTP %d", in.Status)
			}
			var response struct {
				Status       string `json:"status"`
				CanCancel    bool   `json:"can_cancel"`
				Cancellation *struct {
					RequestedAt string `json:"requested_at"`
					RequestedBy string `json:"requested_by"`
					Reason      string `json:"reason"`
				} `json:"cancellation"`
			}
			if err := json.Unmarshal(in.Body, &response); err != nil {
				return err
			}
			if response.Status != "running" || response.CanCancel || response.Cancellation == nil || response.Cancellation.RequestedAt == "" || response.Cancellation.RequestedBy == "" || response.Cancellation.Reason != "first private reason" {
				return fmt.Errorf("authorized Run detail omitted or misrepresented cancellation")
			}
			return nil
		}),
	}
}
func (in RunCancellationAPI) request(method, path string, body []byte) (RunCancellationAPI, error) {
	req, err := http.NewRequest(method, in.Auth.URL+path, bytes.NewReader(body))
	if err != nil {
		return in, err
	}
	req.Header.Set("Content-Type", "application/json")
	if in.Token.Value != "" {
		req.Header.Set("Authorization", in.Token.Type+" "+in.Token.Value)
	}
	response, err := in.Auth.Client.Do(req)
	if err != nil {
		return in, err
	}
	defer response.Body.Close()
	in.Status = response.StatusCode
	in.Body, err = io.ReadAll(response.Body)
	return in, err
}
func cancellationRequestBody(kind string) ([]byte, error) {
	bodies := map[string]string{
		"valid":           `{"reason":"first private reason"}`,
		"replacement":     `{"reason":"replacement reason"}`,
		"forged owner":    `{"requester":"somebody-else"}`,
		"forged time":     `{"requested_at":"2026-01-01T00:00:00Z"}`,
		"forced status":   `{"status":"aborted"}`,
		"duplicate field": `{"reason":"one","reason":"two"}`,
		"wrong case":      `{"Reason":"private reason"}`,
		"null reason":     `{"reason":null}`,
		"control reason":  `{"reason":"private\nreason"}`,
		"oversized":       `{"reason":"` + strings.Repeat("x", 513) + `"}`,
		"trailing JSON":   `{} {}`,
		"invalid UTF-8":   "{\"reason\":\"" + string([]byte{255}) + "\"}",
	}
	body, ok := bodies[kind]
	if !ok {
		return nil, fmt.Errorf("unknown request case")
	}
	return []byte(body), nil
}
