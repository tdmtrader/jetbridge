package steps

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db/migration"
)

const (
	buildEventsPreMigration  = 1602860421
	buildEventsPostMigration = 1606068653
	buildEventsDownMigration = 1603405319
)

type MigrationBuildEventsBigintObservation struct {
	Profile string
	Failure string
}

func MigrationBuildEventsBigintStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MigrationBuildEventsBigintObservation](
			"the actual build event bigint migration evaluates profile {string}",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (MigrationBuildEventsBigintObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return MigrationBuildEventsBigintObservation{}, fmt.Errorf("expected build event migration profile")
				}
				postgres, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return MigrationBuildEventsBigintObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return MigrationBuildEventsBigintObservation{Profile: profile, Failure: observeMigrationBuildEventsBigint(postgres, profile)}, nil
			},
		),
		brine.DefineCheck[MigrationBuildEventsBigintObservation](
			"the build event bigint migration observation exactly matches {string}",
			func(observation MigrationBuildEventsBigintObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected build event migration profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("build event bigint migration observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeMigrationBuildEventsBigint(postgres *postmaster, profile string) string {
	valid := map[string]bool{
		"up-parent": true, "up-existing-pipeline": true, "up-existing-team": true,
		"up-new-team": true, "up-new-pipeline": true, "down-parent": true,
		"down-existing-pipeline-old": true, "down-existing-pipeline-new": true,
		"down-existing-team": true, "down-new-team": true, "down-new-pipeline": true,
	}
	if !valid[profile] {
		return fmt.Sprintf("unknown profile %q", profile)
	}

	runner := postgres.runner
	runner.CreateEmptyTestDB()
	defer runner.DropTestDB()
	database, err := runner.TryOpenDBAtVersion(buildEventsPreMigration)
	if err != nil {
		return fmt.Sprintf("open at pre-migration version: %v", err)
	}
	defer database.Close()

	var teamID, pipelineID int
	if err := database.QueryRow(`INSERT INTO teams (name, auth) VALUES ('some-team', '{}') RETURNING id`).Scan(&teamID); err != nil {
		return err.Error()
	}
	if err := database.QueryRow(`INSERT INTO pipelines (name, team_id) VALUES ('some-pipeline', $1) RETURNING id`, teamID).Scan(&pipelineID); err != nil {
		return err.Error()
	}
	helper := migration.NewOpenHelper("pgx", runner.DataSourceName(), nil, nil, nil)
	if err := helper.MigrateToVersion(buildEventsPostMigration); err != nil {
		return fmt.Sprintf("migrate up: %v", err)
	}
	if strings.HasPrefix(profile, "down-") {
		if err := helper.MigrateToVersion(buildEventsDownMigration); err != nil {
			return fmt.Sprintf("migrate down: %v", err)
		}
	}

	switch profile {
	case "up-parent":
		return requireMigrationPlans(database,
			migrationPlan{"parent build_id", `SELECT * FROM build_events WHERE build_id = 1`, "Index Scan", ""},
			migrationPlan{"parent build_id_old", `SELECT * FROM build_events WHERE build_id_old = 1`, "Index Scan", ""},
		)
	case "up-existing-pipeline":
		table := fmt.Sprintf("pipeline_build_events_%d", pipelineID)
		return requireMigrationPlans(database,
			migrationPlan{"existing pipeline build_id", fmt.Sprintf(`SELECT * FROM %s WHERE build_id = 1`, table), "Index Scan", ""},
			migrationPlan{"existing pipeline build_id_old", fmt.Sprintf(`SELECT * FROM %s WHERE build_id_old = 1`, table), "Index Scan", ""},
		)
	case "up-existing-team":
		table := fmt.Sprintf("team_build_events_%d", teamID)
		return requireMigrationPlans(database,
			migrationPlan{"existing team build_id", fmt.Sprintf(`SELECT * FROM %s WHERE build_id = 1`, table), "Index Scan", ""},
			migrationPlan{"existing team build_id_old", fmt.Sprintf(`SELECT * FROM %s WHERE build_id_old = 1`, table), "Seq Scan", ""},
		)
	case "up-new-team":
		var newTeamID int
		if err := database.QueryRow(`INSERT INTO teams (name, auth) VALUES ('some-other-team', '{}') RETURNING id`).Scan(&newTeamID); err != nil {
			return err.Error()
		}
		return requireMigrationPlans(database, migrationPlan{"new team build_id", fmt.Sprintf(`SELECT * FROM team_build_events_%d WHERE build_id = 1`, newTeamID), "Index Scan", ""})
	case "up-new-pipeline":
		var newPipelineID int
		if err := database.QueryRow(`INSERT INTO pipelines (name, team_id) VALUES ('some-other-pipeline', $1) RETURNING id`, teamID).Scan(&newPipelineID); err != nil {
			return err.Error()
		}
		table := fmt.Sprintf("pipeline_build_events_%d", newPipelineID)
		return requireMigrationPlans(database,
			migrationPlan{"new pipeline build_id", fmt.Sprintf(`SELECT * FROM %s WHERE build_id = 1`, table), "Index Scan", ""},
			migrationPlan{"new pipeline build_id_old", fmt.Sprintf(`SELECT * FROM %s WHERE build_id_old = 1`, table), "Index Scan", ""},
		)
	case "down-parent":
		return requireMigrationPlans(database, migrationPlan{"down parent build_id", `SELECT * FROM build_events WHERE build_id = 1`, "Index Scan", "build_id_old"})
	case "down-existing-pipeline-old":
		return requireMigrationPlans(database, migrationPlan{"down existing pipeline old", fmt.Sprintf(`SELECT * FROM pipeline_build_events_%d WHERE build_id_old = 1`, pipelineID), "Index Scan", ""})
	case "down-existing-pipeline-new":
		return requireMigrationPlans(database, migrationPlan{"down existing pipeline new", fmt.Sprintf(`SELECT * FROM pipeline_build_events_%d WHERE build_id = 1`, pipelineID), "Seq Scan", ""})
	case "down-existing-team":
		return requireMigrationPlans(database, migrationPlan{"down existing team", fmt.Sprintf(`SELECT * FROM team_build_events_%d WHERE build_id = 1`, teamID), "Seq Scan", ""})
	case "down-new-team":
		var newTeamID int
		if err := database.QueryRow(`INSERT INTO teams (name, auth) VALUES ('some-other-team', '{}') RETURNING id`).Scan(&newTeamID); err != nil {
			return err.Error()
		}
		return requireMigrationPlans(database, migrationPlan{"down new team", fmt.Sprintf(`SELECT * FROM team_build_events_%d WHERE build_id = 1`, newTeamID), "Seq Scan", ""})
	case "down-new-pipeline":
		var newPipelineID int
		if err := database.QueryRow(`INSERT INTO pipelines (name, team_id) VALUES ('some-other-pipeline', $1) RETURNING id`, teamID).Scan(&newPipelineID); err != nil {
			return err.Error()
		}
		return requireMigrationPlans(database, migrationPlan{"down new pipeline", fmt.Sprintf(`SELECT * FROM pipeline_build_events_%d WHERE build_id = 1`, newPipelineID), "Index Scan", ""})
	}
	return ""
}

type migrationPlan struct {
	name   string
	query  string
	want   string
	absent string
}

func requireMigrationPlans(database *sql.DB, plans ...migrationPlan) string {
	if _, err := database.Exec("SET enable_seqscan = OFF"); err != nil {
		return err.Error()
	}
	for _, expectation := range plans {
		rows, err := database.Query("EXPLAIN " + expectation.query)
		if err != nil {
			return fmt.Sprintf("%s: %v", expectation.name, err)
		}
		lines := []string{}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				return fmt.Sprintf("%s: %v", expectation.name, err)
			}
			lines = append(lines, line)
		}
		if err := rows.Close(); err != nil {
			return fmt.Sprintf("%s: %v", expectation.name, err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, expectation.want) {
			return fmt.Sprintf("%s plan=%q, want %q", expectation.name, plan, expectation.want)
		}
		if expectation.absent != "" && strings.Contains(plan, expectation.absent) {
			return fmt.Sprintf("%s plan=%q unexpectedly contains %q", expectation.name, plan, expectation.absent)
		}
	}
	return ""
}
