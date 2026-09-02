package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

type schedulerAlgorithmProbe struct {
	example Example
	err     error
}

var (
	dbConn      db.DbConn
	teamFactory db.TeamFactory
)

func SchedulerAlgorithmStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *schedulerAlgorithmProbe](
			"the production scheduler algorithm example {string} uses a real database",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (*schedulerAlgorithmProbe, error) {
				name, err := paramAt("the production scheduler algorithm example {string} uses a real database", p, 0)
				if err != nil {
					return nil, err
				}
				example, found := schedulerAlgorithmExamples[name]
				if !found {
					return nil, fmt.Errorf("unknown scheduler algorithm example %q", name)
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				dbConn = database.Conn
				teamFactory = database.TeamFactory
				return &schedulerAlgorithmProbe{example: example}, nil
			},
		),

		brine.DefineMap[*schedulerAlgorithmProbe, *schedulerAlgorithmProbe](
			"the production input algorithm resolves the example",
			func(in *schedulerAlgorithmProbe, _ brine.Params, _ *brine.Recorder) (_ *schedulerAlgorithmProbe, err error) {
				defer func() {
					if recovered := recover(); recovered != nil {
						err = fmt.Errorf("production algorithm result differs: %v", recovered)
					}
				}()
				in.example.Run()
				return in, nil
			},
		),

		CheckThat[*schedulerAlgorithmProbe](
			"the resolution matches the source contract",
			func(in *schedulerAlgorithmProbe) error { return in.err },
		),
	}
}
