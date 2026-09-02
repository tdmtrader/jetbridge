package steps

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db/migration"
)

const (
	authProvidersPreMigration  = 1513895878
	authProvidersPostMigration = 1516643303
)

type MigrationUpdateAuthProvidersObservation struct {
	Profile string
	Failure string
}

func MigrationUpdateAuthProvidersStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MigrationUpdateAuthProvidersObservation](
			"the production auth providers migration evaluates profile {string}",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (MigrationUpdateAuthProvidersObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return MigrationUpdateAuthProvidersObservation{}, fmt.Errorf("expected auth providers migration profile")
				}
				postgres, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return MigrationUpdateAuthProvidersObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return MigrationUpdateAuthProvidersObservation{Profile: profile, Failure: observeMigrationUpdateAuthProviders(postgres, profile)}, nil
			},
		),
		brine.DefineCheck[MigrationUpdateAuthProvidersObservation](
			"the auth providers migration observation exactly matches {string}",
			func(observation MigrationUpdateAuthProvidersObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected auth providers migration profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("auth providers migration observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeMigrationUpdateAuthProviders(postgres *postmaster, profile string) string {
	valid := map[string]bool{
		"down-basic-fields": true, "down-preserves-github": true,
		"down-removes-basic-provider": true, "down-removes-noauth-provider": true,
		"up-basic-empty-auth": true, "up-basic-null-auth": true,
		"up-merges-basic-and-github": true, "up-rejects-malformed-basic": true,
		"up-rejects-empty-basic": true, "up-basic-does-not-add-noauth": true,
		"up-provider-does-not-add-noauth": true, "up-empty-adds-noauth": true,
	}
	if !valid[profile] {
		return fmt.Sprintf("unknown profile %q", profile)
	}

	runner := postgres.runner
	runner.CreateEmptyTestDB()
	defer runner.DropTestDB()
	if profile[:5] == "down-" {
		return observeAuthProvidersDown(runner, profile)
	}
	return observeAuthProvidersUp(runner, profile)
}

type authProvidersRunner interface {
	TryOpenDBAtVersion(int) (*sql.DB, error)
	DataSourceName() string
}

func observeAuthProvidersDown(runner authProvidersRunner, profile string) string {
	database, err := runner.TryOpenDBAtVersion(authProvidersPostMigration)
	if err != nil {
		return fmt.Sprintf("open at post-migration version: %v", err)
	}
	defer database.Close()

	auth := `{"basicauth":{"username":"username","password":"password"}}`
	if profile == "down-preserves-github" {
		auth = `{"github":{"client_id":"some-client-id","client_secret":"some-client-secret"}}`
	}
	if profile == "down-removes-noauth-provider" {
		auth = `{"noauth":{"noauth":true}}`
	}
	if _, err := database.Exec(`INSERT INTO teams (name, auth) VALUES ('main', $1)`, auth); err != nil {
		return err.Error()
	}

	helper := migration.NewOpenHelper("pgx", runner.DataSourceName(), nil, nil, nil)
	if err := helper.MigrateToVersion(authProvidersPreMigration); err != nil {
		return fmt.Sprintf("migrate down: %v", err)
	}

	switch profile {
	case "down-basic-fields":
		basic, err := readAuthProvidersJSON(database, "basic_auth", "main")
		if err != nil {
			return err.Error()
		}
		if basic["basic_auth_username"] != "username" || basic["basic_auth_password"] != "password" {
			return fmt.Sprintf("basic auth got %#v", basic)
		}
	case "down-preserves-github":
		providers, err := readAuthProvidersJSON(database, "auth", "main")
		if err != nil {
			return err.Error()
		}
		github, _ := providers["github"].(map[string]any)
		if github["client_id"] != "some-client-id" || github["client_secret"] != "some-client-secret" {
			return fmt.Sprintf("github provider got %#v", github)
		}
	case "down-removes-basic-provider":
		return requireAuthProviderAbsent(database, "main", "basicauth")
	case "down-removes-noauth-provider":
		return requireAuthProviderAbsent(database, "main", "noauth")
	}
	return ""
}

func observeAuthProvidersUp(runner authProvidersRunner, profile string) string {
	database, err := runner.TryOpenDBAtVersion(authProvidersPreMigration)
	if err != nil {
		return fmt.Sprintf("open at pre-migration version: %v", err)
	}
	defer database.Close()

	insert := func(name, basicAuth, auth string) error {
		_, err := database.Exec(`INSERT INTO teams (name, basic_auth, auth) VALUES ($1, $2, $3)`, name, basicAuth, auth)
		return err
	}
	switch profile {
	case "up-basic-empty-auth", "up-basic-does-not-add-noauth":
		err = insert("main", `{"basic_auth_username":"username","basic_auth_password":"password"}`, ``)
	case "up-basic-null-auth":
		err = insert("main", `{"basic_auth_username":"username","basic_auth_password":"password"}`, `null`)
	case "up-merges-basic-and-github":
		err = insert("main", `{"basic_auth_username":"username","basic_auth_password":"password"}`, `{"github":{"client_id":"some-client-id","client_secret":"some-client-secret"}}`)
	case "up-rejects-malformed-basic":
		err = insert("main", `{"u":"username","p":"password"}`, ``)
	case "up-rejects-empty-basic":
		if err = insert("main-empty", `{}`, ``); err == nil {
			err = insert("main-null", `null`, ``)
		}
	case "up-provider-does-not-add-noauth":
		err = insert("main", `{}`, `{"github":{"client_id":"some-client-id","client_secret":"some-client-secret"}}`)
	case "up-empty-adds-noauth":
		if err = insert("main-empty-blank", `{}`, ``); err == nil {
			err = insert("main-null-blank", `null`, ``)
		}
		if err == nil {
			err = insert("main-empty-empty", `{}`, `{}`)
		}
	}
	if err != nil {
		return err.Error()
	}

	helper := migration.NewOpenHelper("pgx", runner.DataSourceName(), nil, nil, nil)
	if err := helper.MigrateToVersion(authProvidersPostMigration); err != nil {
		return fmt.Sprintf("migrate up: %v", err)
	}

	switch profile {
	case "up-basic-empty-auth", "up-basic-null-auth", "up-merges-basic-and-github":
		providers, err := readAuthProvidersJSON(database, "auth", "main")
		if err != nil {
			return err.Error()
		}
		basic, _ := providers["basicauth"].(map[string]any)
		if basic["username"] != "username" || basic["password"] != "password" {
			return fmt.Sprintf("basicauth provider got %#v", basic)
		}
		if profile == "up-merges-basic-and-github" {
			github, _ := providers["github"].(map[string]any)
			if github["client_id"] != "some-client-id" || github["client_secret"] != "some-client-secret" {
				return fmt.Sprintf("github provider got %#v", github)
			}
		}
	case "up-rejects-malformed-basic":
		return requireAuthProviderAbsent(database, "main", "basicauth")
	case "up-rejects-empty-basic":
		if failure := requireAuthProviderAbsent(database, "main-empty", "basicauth"); failure != "" {
			return failure
		}
		return requireAuthProviderAbsent(database, "main-null", "basicauth")
	case "up-basic-does-not-add-noauth", "up-provider-does-not-add-noauth":
		return requireAuthProviderAbsent(database, "main", "noauth")
	case "up-empty-adds-noauth":
		for _, name := range []string{"main-empty-blank", "main-null-blank", "main-empty-empty"} {
			providers, err := readAuthProvidersJSON(database, "auth", name)
			if err != nil {
				return err.Error()
			}
			noauth, _ := providers["noauth"].(map[string]any)
			if noauth["noauth"] != true {
				return fmt.Sprintf("%s noauth provider got %#v", name, noauth)
			}
		}
	}
	return ""
}

func requireAuthProviderAbsent(database *sql.DB, team, provider string) string {
	providers, err := readAuthProvidersJSON(database, "auth", team)
	if err != nil {
		return err.Error()
	}
	if providers[provider] != nil {
		return fmt.Sprintf("%s provider remained %#v", provider, providers[provider])
	}
	return ""
}

func readAuthProvidersJSON(database *sql.DB, column, team string) (map[string]any, error) {
	var raw []byte
	if err := database.QueryRow("SELECT "+column+" FROM teams WHERE name = $1", team).Scan(&raw); err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
