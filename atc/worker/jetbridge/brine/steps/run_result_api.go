package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// Read through the real issuer and protected HTTP handler. The publication is
// still the one produced by the runtime/capture fixture, not a seeded response.
func RunResultAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunResultPublication, RunResultPublication]("a fresh {string} client reads the retained Run result", []string{"auth-server"}, func(in RunResultPublication, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunResultPublication, error) {
			caller, _ := p.GetString(0)
			auth, err := authServer(res)
			if err != nil {
				return in, err
			}
			team, found, err := in.Start.DB.TeamFactory.FindTeam("output-start")
			if err != nil || !found {
				return in, fmt.Errorf("load result team (found %t): %w", found, err)
			}
			if err := team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner"}}, "viewer": {"users": {"local:viewer"}}}); err != nil {
				return in, err
			}
			base, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
			if err != nil || !found {
				return in, fmt.Errorf("load result template (found %t): %w", found, err)
			}
			if err = base.Expose(); err != nil {
				return in, err
			}
			client := RunCancellationAPI{Start: in.Start, Auth: auth}
			if caller != "anonymous" {
				user := caller
				if caller == "cross-team" {
					// Owner is authenticated but loses membership of the result's team.
					user = "owner"
					if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:another-owner"}}}); err != nil {
						return in, err
					}
				}
				if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", user, "-p", authPassword); err != nil {
					return in, err
				}
				client.Token, err = auth.savedFlyToken()
				if err != nil {
					return in, err
				}
			}
			client, err = client.request(http.MethodGet, fmt.Sprintf("/api/v1/teams/output-start/pipelines/review/runs/%d", in.Start.Creation.Run.Number()), nil)
			if err != nil {
				return in, err
			}
			if client.Status != http.StatusOK {
				return in, fmt.Errorf("Run result detail returned HTTP %d", client.Status)
			}
			var wire map[string]json.RawMessage
			if err = json.Unmarshal(client.Body, &wire); err != nil {
				return in, err
			}
			body, present := wire["terminal"]
			if caller == "anonymous" || caller == "cross-team" {
				if _, capture := wire["captures"]; present || capture {
					return in, fmt.Errorf("unauthorized detail disclosed retained results or capture progress")
				}
				return in, nil
			}
			var captures []struct {
				TaskID string `json:"task_id"`
				Result string `json:"result"`
				Events []struct {
					Kind   string `json:"kind"`
					Reason string `json:"reason,omitempty"`
				} `json:"events"`
			}
			if err = json.Unmarshal(wire["captures"], &captures); err != nil {
				return in, fmt.Errorf("authorized Run detail omitted capture progress: %w", err)
			}
			if len(captures) != 1 || captures[0].TaskID == "" || captures[0].Result == "" || len(captures[0].Events) != 3 {
				return in, fmt.Errorf("Run capture progress did not retain its task, result and three ordered events")
			}
			for i, kind := range []string{"capture-selected", "capture-seal-started", "capture-disposition"} {
				if captures[0].Events[i].Kind != kind {
					return in, fmt.Errorf("Run capture event %d did not retain its emission order", i)
				}
			}
			if captures[0].Events[2].Reason != "captured" {
				return in, fmt.Errorf("Run capture progress omitted the actual disposition")
			}
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			retained, terminal, err := factory.TerminalResult(context.Background(), in.Start.Creation.Run.ID())
			if err != nil {
				return in, err
			}
			if !terminal {
				if present {
					return in, fmt.Errorf("running Run exposed its hidden candidate")
				}
				return in, nil
			}
			if !present {
				return in, fmt.Errorf("authorized Run detail omitted the retained terminal observation")
			}
			var result db.RunTerminalResult
			if err = json.Unmarshal(body, &result); err != nil {
				return in, err
			}
			if result.Results == nil || result.Version == "" || result.Status != retained.Status || result.Version != retained.Version || !result.CompletedAt.Equal(retained.CompletedAt) || !reflect.DeepEqual(result.Results, retained.Results) {
				return in, fmt.Errorf("HTTP result differs from the immutable terminal observation")
			}
			// The public type consumed by the existing SDK must retain this field.
			var run atc.PipelineRun
			if err = json.Unmarshal(client.Body, &run); err != nil {
				return in, err
			}
			roundTrip, err := json.Marshal(run)
			if err != nil {
				return in, err
			}
			var decoded map[string]json.RawMessage
			if err = json.Unmarshal(roundTrip, &decoded); err != nil {
				return in, err
			}
			if _, found := decoded["terminal"]; !found {
				return in, fmt.Errorf("the public client type discarded the terminal result")
			}
			if run.Status != result.Status || run.CompletedAt == nil || run.CompletedAt.Unix() != result.CompletedAt.Unix() {
				return in, fmt.Errorf("detail status and terminal observation disagree")
			}
			return in, nil
		}),
	}
}
