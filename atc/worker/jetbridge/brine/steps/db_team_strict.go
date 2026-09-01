package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

type DBTeamStrictObservation struct{ Value string }

func DBTeamStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBTeamStrictObservation](
			"the strict real DB team evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBTeamStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBTeamStrictObservation{}, fmt.Errorf("expected strict DB team profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBTeamStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeDBTeamStrict(database, profile)
				return DBTeamStrictObservation{Value: value}, err
			},
		),
		CheckString[DBTeamStrictObservation](
			"the strict DB team observation is {string}",
			"strict DB team observation",
			func(observation DBTeamStrictObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeDBTeamStrict(database JetbridgeDB, profile string) (string, error) {
	sourceProfile := profile
	switch profile {
	case "delete/team", "delete/event-table":
		sourceProfile = "delete"
	}
	value, err := observeTeamDomain(database, sourceProfile)
	if err != nil {
		return "", err
	}
	switch profile {
	case "delete/team":
		return strictDBTeamField(value, "found"), nil
	case "delete/event-table":
		return strictDBTeamField(value, "event-table"), nil
	default:
		return value, nil
	}
}

func strictDBTeamField(observation, key string) string {
	for _, field := range strings.Split(observation, ";") {
		if strings.HasPrefix(field, key+"=") {
			return field
		}
	}
	return ""
}
