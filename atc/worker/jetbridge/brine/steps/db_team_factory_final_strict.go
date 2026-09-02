package steps

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBTeamFactoryFinalObservation struct{ Value string }

var finalTeamAuth = atc.TeamAuth{
	"owner": {"users": []string{"local:username"}},
}

func DBTeamFactoryFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBTeamFactoryFinalObservation](
			"the production team factory evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBTeamFactoryFinalObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBTeamFactoryFinalObservation{}, fmt.Errorf("expected team factory profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBTeamFactoryFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeDBTeamFactoryFinal(database, profile)
				if err != nil {
					return DBTeamFactoryFinalObservation{}, fmt.Errorf("team factory observation: %w", err)
				}
				return DBTeamFactoryFinalObservation{Value: value}, nil
			},
		),
		CheckString[DBTeamFactoryFinalObservation](
			"the team factory observation is {string}",
			"team factory observation",
			func(observation DBTeamFactoryFinalObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeDBTeamFactoryFinal(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "create":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team", Auth: finalTeamAuth})
		if err != nil {
			return "", err
		}
		foundTeam, found, err := database.TeamFactory.FindTeam("some-team")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("name=%s;auth=%t;found=%t;same-id=%t", team.Name(), equalFinalTeamAuth(team.Auth()), found, found && foundTeam.ID() == team.ID()), nil
	case "find-existing":
		if _, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team", Auth: finalTeamAuth}); err != nil {
			return "", err
		}
		team, found, err := database.TeamFactory.FindTeam("some-team")
		if err != nil {
			return "", err
		}
		if !found || team == nil {
			return "", fmt.Errorf("persisted team was not found")
		}
		return fmt.Sprintf("name=%s;auth=%t", team.Name(), equalFinalTeamAuth(team.Auth())), nil
	case "find-missing":
		team, found, err := database.TeamFactory.FindTeam("some-team")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("nil=%t;found=%t", team == nil, found), nil
	case "default-create":
		if err := removeFinalDefaultTeam(database.TeamFactory); err != nil {
			return "", err
		}
		team, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
		if err != nil {
			return "", err
		}
		if team == nil {
			return "", fmt.Errorf("created default team is nil")
		}
		foundTeam, found, err := database.TeamFactory.FindTeam(atc.DefaultTeamName)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("after-admin=%t;found=%t;same-id=%t", team.Admin(), found, found && foundTeam.ID() == team.ID()), nil
	case "default-idempotent":
		if err := removeFinalDefaultTeam(database.TeamFactory); err != nil {
			return "", err
		}
		first, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
		if err != nil {
			return "", err
		}
		if first == nil {
			return "", fmt.Errorf("first default team is nil")
		}
		second, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("same-id=%t", second != nil && first.ID() == second.ID()), nil
	case "list-one", "list-two":
		if err := removeFinalDefaultTeam(database.TeamFactory); err != nil {
			return "", err
		}
		if _, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team", Auth: finalTeamAuth}); err != nil {
			return "", err
		}
		if profile == "list-two" {
			if _, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-other-team"}); err != nil {
				return "", err
			}
		}
		teams, err := database.TeamFactory.GetTeams()
		if err != nil {
			return "", err
		}
		names := make([]string, len(teams))
		for i, team := range teams {
			names[i] = team.Name()
		}
		authOK := len(teams) > 0 && equalFinalTeamAuth(teams[len(teams)-1].Auth())
		return fmt.Sprintf("count=%d;names=%s;auth=%t", len(teams), strings.Join(names, ","), authOK), nil
	default:
		return "", fmt.Errorf("unknown team factory profile %q", profile)
	}
}

func removeFinalDefaultTeam(factory db.TeamFactory) error {
	team, found, err := factory.FindTeam(atc.DefaultTeamName)
	if err != nil {
		return err
	}
	if found {
		return team.Delete()
	}
	return nil
}

func equalFinalTeamAuth(actual atc.TeamAuth) bool {
	return reflect.DeepEqual(actual, finalTeamAuth)
}
