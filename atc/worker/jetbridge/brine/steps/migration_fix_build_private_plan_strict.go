package steps

import (
	"database/sql"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db/migration"
)

const (
	fixBuildPlanPreMigration  = 1551384519
	fixBuildPlanPostMigration = 1551384520
)

type MigrationFixBuildPlanObservation struct {
	Profile string
	Failure string
}

func MigrationFixBuildPlanStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MigrationFixBuildPlanObservation](
			"the actual build private plan migration evaluates profile {string}",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (MigrationFixBuildPlanObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return MigrationFixBuildPlanObservation{}, fmt.Errorf("expected build private plan migration profile")
				}
				postgres, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return MigrationFixBuildPlanObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return MigrationFixBuildPlanObservation{Profile: profile, Failure: observeMigrationFixBuildPlan(postgres, profile)}, nil
			},
		),
		brine.DefineCheck[MigrationFixBuildPlanObservation](
			"the build private plan migration observation exactly matches {string}",
			func(observation MigrationFixBuildPlanObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected build private plan migration profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("build private plan migration observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeMigrationFixBuildPlan(postgres *postmaster, profile string) string {
	valid := map[string]bool{
		"up-null": true, "up-plan": true, "up-multiple": true,
		"down-null": true, "down-plan": true, "down-multiple": true,
	}
	if !valid[profile] {
		return fmt.Sprintf("unknown profile %q", profile)
	}

	runner := postgres.runner
	runner.CreateEmptyTestDB()
	defer runner.DropTestDB()

	directionDown := profile == "down-null" || profile == "down-plan" || profile == "down-multiple"
	startVersion := fixBuildPlanPreMigration
	targetVersion := fixBuildPlanPostMigration
	if directionDown {
		startVersion, targetVersion = targetVersion, startVersion
	}
	database, err := runner.TryOpenDBAtVersion(startVersion)
	if err != nil {
		return fmt.Sprintf("open at starting version: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO teams (name, auth) VALUES ('some-team', '{}')`); err != nil {
		database.Close()
		return err.Error()
	}

	switch profile {
	case "up-null", "down-null":
		err = insertMigrationBuild(database, "some-build", nil)
	case "up-plan":
		err = insertMigrationBuild(database, "some-build", stringPointer(`{"plan":{"some":"plan"}}`))
	case "up-multiple":
		if err = insertMigrationBuild(database, "some-build", stringPointer(`{"plan":{"some":"plan"}}`)); err == nil {
			err = insertMigrationBuild(database, "some-other-build", stringPointer(`{"plan":{"some":"other-plan"}}`))
		}
	case "down-plan":
		err = insertMigrationBuild(database, "some-build", stringPointer(`{"some":"plan"}`))
	case "down-multiple":
		if err = insertMigrationBuild(database, "some-build", stringPointer(`{"some":"plan"}`)); err == nil {
			err = insertMigrationBuild(database, "some-other-build", stringPointer(`{"some":"other-plan"}`))
		}
	}
	if err != nil {
		database.Close()
		return err.Error()
	}
	if err := database.Close(); err != nil {
		return err.Error()
	}

	helper := migration.NewOpenHelper("pgx", runner.DataSourceName(), nil, nil, nil)
	if err := helper.MigrateToVersion(targetVersion); err != nil {
		return fmt.Sprintf("migrate to target version: %v", err)
	}
	database, err = runner.TryOpenDBAtVersion(targetVersion)
	if err != nil {
		return fmt.Sprintf("open at target version: %v", err)
	}
	defer database.Close()

	switch profile {
	case "up-null", "down-null":
		return requireMigrationBuildPlan(database, "some-build", nil)
	case "up-plan":
		return requireMigrationBuildPlan(database, "some-build", stringPointer(`{"some":"plan"}`))
	case "up-multiple":
		if failure := requireMigrationBuildPlan(database, "some-build", stringPointer(`{"some":"plan"}`)); failure != "" {
			return failure
		}
		return requireMigrationBuildPlan(database, "some-other-build", stringPointer(`{"some":"other-plan"}`))
	case "down-plan":
		return requireMigrationBuildPlan(database, "some-build", stringPointer(`{"plan":{"some":"plan"}}`))
	case "down-multiple":
		if failure := requireMigrationBuildPlan(database, "some-build", stringPointer(`{"plan":{"some":"plan"}}`)); failure != "" {
			return failure
		}
		return requireMigrationBuildPlan(database, "some-other-build", stringPointer(`{"plan":{"some":"other-plan"}}`))
	}
	return ""
}

func stringPointer(value string) *string { return &value }

func insertMigrationBuild(database *sql.DB, name string, plan *string) error {
	_, err := database.Exec("INSERT INTO builds(name, status, team_id, private_plan) VALUES($1, 'started', 1, $2)", name, plan)
	return err
}

func requireMigrationBuildPlan(database *sql.DB, name string, expected *string) string {
	var plan sql.NullString
	if err := database.QueryRow("SELECT private_plan FROM builds WHERE name = $1", name).Scan(&plan); err != nil {
		return err.Error()
	}
	if expected == nil {
		if plan.Valid {
			return fmt.Sprintf("build %q plan got %q, want NULL", name, plan.String)
		}
		return ""
	}
	if !plan.Valid {
		return fmt.Sprintf("build %q plan got NULL, want %q", name, *expected)
	}
	if plan.String != *expected {
		return fmt.Sprintf("build %q plan got %q, want %q", name, plan.String, *expected)
	}
	return ""
}
