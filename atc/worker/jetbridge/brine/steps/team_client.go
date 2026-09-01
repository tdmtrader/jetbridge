package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type TeamClientState struct {
	API      *PipelineAPI
	Client   clientapi.Client
	Admin    clientapi.Team
	Found    bool
	Err      error
	Created  bool
	Updated  bool
	Warnings int
	Names    []string
	Status   int
}

func TeamClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *TeamClientState](
			"the production Go team client, real API, and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, resources brine.Resources) (*TeamClientState, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				api, err := newPipelineAPI(database, rec)
				if err != nil {
					return nil, err
				}
				client := clientapi.NewClient(api.Server.URL, api.Client, false)
				return &TeamClientState{API: api, Client: client, Admin: client.Team("api-team")}, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the team API has persisted teams {string}",
			func(in *TeamClientState, p brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				raw, _ := p.GetString(0)
				for _, name := range strings.Split(raw, ",") {
					_, err := in.API.DB.TeamFactory.CreateTeam(atc.Team{Name: strings.TrimSpace(name), Auth: atc.TeamAuth{"owner": {}}})
					if err != nil {
						return in, err
					}
				}
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the Go client lists teams",
			func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				teams, err := in.Client.ListTeams()
				in.Err, in.Names = err, nil
				for _, team := range teams {
					in.Names = append(in.Names, team.Name)
				}
				sort.Strings(in.Names)
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the Go client finds team {string}",
			func(in *TeamClientState, p brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				name, _ := p.GetString(0)
				team, err := in.Client.FindTeam(name)
				in.Err, in.Found, in.Names = err, team != nil, nil
				if team != nil {
					in.Names = []string{team.Name()}
				}
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the Go client {string} team {string}",
			func(in *TeamClientState, p brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				mode, name, err := twoParams("the Go client {string} team {string}", p)
				if err != nil {
					return in, err
				}
				clientTeam := in.Client.Team(name)
				passed := validClientTeam(name)
				if mode == "updates" {
					if _, _, _, _, err := clientTeam.CreateOrUpdate(passed); err != nil {
						return in, err
					}
				} else if mode != "creates" {
					return in, fmt.Errorf("unknown team save mode %q", mode)
				}
				returned, created, updated, warnings, err := clientTeam.CreateOrUpdate(passed)
				in.Created, in.Updated, in.Warnings, in.Err = created, updated, len(warnings), err
				in.Names = []string{returned.Name}
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the Go client creates warning-named team",
			func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				team := in.Client.Team("-warning")
				var warnings []clientapi.ConfigWarning
				_, in.Created, in.Updated, warnings, in.Err = team.CreateOrUpdate(validClientTeam("-warning"))
				in.Warnings = len(warnings)
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the Go client destroys team {string}",
			func(in *TeamClientState, p brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				name, _ := p.GetString(0)
				in.Err = in.Admin.DestroyTeam(name)
				_, found, err := in.API.DB.TeamFactory.FindTeam(name)
				if err != nil {
					return in, err
				}
				in.Found = found
				return in, nil
			},
		),
		brine.DefineMap[*TeamClientState, *TeamClientState](
			"the team API deletes missing team",
			func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) (*TeamClientState, error) {
				if err := in.API.request("DELETE", "/api/v1/teams/missing", nil); err != nil {
					return in, err
				}
				in.Status = in.API.Status
				return in, nil
			},
		),
		brine.DefineCheck[*TeamClientState]("the Go team client returned no error", func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) error { return in.Err }),
		brine.DefineCheck[*TeamClientState]("the Go team client returned an error", func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Err == nil {
				return fmt.Errorf("team client returned no error")
			}
			return nil
		}),
		brine.DefineCheck[*TeamClientState]("the Go team client found the team", func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.Found {
				return fmt.Errorf("team client did not find team")
			}
			return nil
		}),
		brine.DefineCheck[*TeamClientState]("the persisted team is absent", func(in *TeamClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Found {
				return fmt.Errorf("team still exists")
			}
			return nil
		}),
		brine.DefineCheck[*TeamClientState]("the Go team client returned teams {string}", func(in *TeamClientState, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if strings.Join(in.Names, ",") != want {
				return fmt.Errorf("expected teams %q, got %q", want, strings.Join(in.Names, ","))
			}
			return nil
		}),
		brine.DefineCheck[*TeamClientState]("the Go team client save result is {string}", func(in *TeamClientState, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			got := fmt.Sprintf("created=%t;updated=%t;warnings=%d", in.Created, in.Updated, in.Warnings)
			if got != want {
				return fmt.Errorf("expected %q, got %q", want, got)
			}
			return nil
		}),
		CheckInt[*TeamClientState]("the team API returned status {int}", "team API status", func(in *TeamClientState) (int, error) { return in.Status, nil }),
	}
}

func validClientTeam(name string) atc.Team {
	return atc.Team{Name: name, Auth: atc.TeamAuth{"owner": {"users": {"brine-user"}}}}
}
