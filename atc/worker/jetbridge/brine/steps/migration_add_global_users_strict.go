package steps

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
)

const (
	addGlobalUsersPreVersion  = 1528314953
	addGlobalUsersPostVersion = 1528470872
)

type AddGlobalUsersMigrationObservation struct {
	Profile string
	Failure string
}

func AddGlobalUsersMigrationStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AddGlobalUsersMigrationObservation](
			"the production add-global-users migration profile {string} is exercised",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AddGlobalUsersMigrationObservation, error) {
				profile, err := paramAt("the production add-global-users migration profile {string} is exercised", p, 0)
				if err != nil {
					return AddGlobalUsersMigrationObservation{}, err
				}
				pm, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return AddGlobalUsersMigrationObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return AddGlobalUsersMigrationObservation{Profile: profile, Failure: observeAddGlobalUsersMigration(pm, profile)}, nil
			},
		),
		brine.DefineCheck[AddGlobalUsersMigrationObservation](
			"the add-global-users migration observation exactly matches {string}",
			func(in AddGlobalUsersMigrationObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the add-global-users migration observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeAddGlobalUsersMigration(pm *postmaster, profile string) string {
	pm.runner.CreateEmptyTestDB()
	defer pm.runner.DropTestDB()

	switch profile {
	case "up-github":
		return observeAddGlobalUsersUp(pm, `{"github":{"client_id":"some-client-id","client_secret":"some-client-secret","organizations":["some-other-org"],"teams":[{"organization_name":"some-org","team_name":"some-team"}],"users":["some-user"]}}`, `{"users":["github:some-user"],"groups":["github:some-org:some-team","github:some-other-org"]}`)
	case "up-basic":
		return observeAddGlobalUsersUp(pm, `{"basicauth":{"username":"some-user","password":"some-password"}}`, `{"users":["local:some-user"],"groups":[]}`)
	case "up-uaa":
		return observeAddGlobalUsersUp(pm, `{"uaa":{"client_id":"some-client-id","client_secret":"some-client-secret","auth_url":"https://example.com/auth","token_url":"https://example.com/token","cf_spaces":["some-space-guid"],"cf_url":"https://example.com/api"}}`, `{"users":[],"groups":["cf:some-space-guid"]}`)
	case "up-gitlab":
		return observeAddGlobalUsersUp(pm, `{"gitlab":{"client_id":"some-client-id","client_secret":"some-client-secret","groups":["some-group"],"auth_url":"https://example.com/auth","token_url":"https://example.com/token","api_url":"https://example.com/api"}}`, `{"users":[],"groups":["gitlab:some-group"]}`)
	case "up-oauth":
		return observeAddGlobalUsersUp(pm, `{"oauth":{"display_name":"provider","client_id":"some-client-id","client_secret":"some-client-secret","auth_url":"https://example.com/auth","token_url":"https://example.com/token","auth_url_params":{"some-param":"some-value"},"scope":"some-scope"}}`, `{"users":[],"groups":["oauth:some-scope"]}`)
	case "up-oidc":
		return observeAddGlobalUsersUp(pm, `{"oauth_oidc":{"display_name":"provider","client_id":"some-client","client_secret":"some-secret","user_id":["some-user"],"groups":["some-group"],"custom_groups_name":"some-groups-key","auth_url":"https://example.com/auth","token_url":"https://example.com/token","auth_url_params":{"some-param":"some-value"},"scope":"some-scope"}}`, `{"users":["oidc:some-user"],"groups":["oidc:some-group"]}`)
	case "reject-bitbucket-cloud":
		return observeAddGlobalUsersRejected(pm, map[string]string{"main": `{"bitbucket-cloud":{"client_id":"some-client","client_secret":"some-client-secret","users":["some-user"],"teams":[{"team_name":"some-team","role":"member"}],"repositories":[{"owner_name":"some-owner","repository_name":"some-repository"}],"auth_url":"https://example.com/auth","token_url":"https://example.com/token","apiurl":"https://example.com/api"}}`})
	case "reject-bitbucket-server":
		return observeAddGlobalUsersRejected(pm, map[string]string{"main": `{"bitbucket-server":{"consumer_key":"/tmp/concourse-dev/keys/web/session_signing_key","private_key":{"N":0,"E":0,"D":0,"Primes":[0,0],"Precomputed":{"Dp":0,"Dq":0,"Qinv":0,"CRTValues":[]}},"endpoint":"https://example.com/endpoint","users":["some-user"],"projects":["some-project"],"repositories":[{"owner_name":"some-owner","repository_name":"some-repository"}]}}`})
	case "reject-uaa-provider-conflict":
		return observeAddGlobalUsersRejected(pm, map[string]string{
			"main":  `{"uaa":{"client_id":"some-client-id","client_secret":"some-client-secret","auth_url":"https://main.com/auth","token_url":"https://main.com/token","cf_spaces":["some-space-guid"],"cf_url":"https://main.com/api"}}`,
			"other": `{"uaa":{"client_id":"some-client-id","client_secret":"some-client-secret","auth_url":"https://other.com/auth","token_url":"https://other.com/token","cf_spaces":["some-space-guid"],"cf_url":"https://other.com/api"}}`,
		})
	case "reject-duplicate-basic-user":
		return observeAddGlobalUsersRejected(pm, map[string]string{
			"main":  `{"basicauth":{"username":"some-user","password":"some-password"}}`,
			"other": `{"basicauth":{"username":"some-user","password":"another-password"}}`,
		})
	case "down-main-only-changed":
		return observeAddGlobalUsersDownAllowed(pm)
	case "down-non-main-changed":
		return observeAddGlobalUsersDownRejected(pm)
	default:
		return fmt.Sprintf("unknown add-global-users migration profile %q", profile)
	}
}

func observeAddGlobalUsersUp(pm *postmaster, legacyAuth, expectedAuth string) string {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalUsersPreVersion)
	if err != nil {
		return fmt.Sprintf("open pre-migration database: %v", err)
	}
	if err := insertAddGlobalUsersTeam(dbConn, "main", legacyAuth); err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("insert legacy team: %v", err))
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close pre-migration database: %v", err)
	}

	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalUsersPostVersion)
	if err != nil {
		return fmt.Sprintf("run add-global-users migration: %v", err)
	}
	auth, err := readAddGlobalUsersColumn(dbConn, "auth", "main")
	if err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("read migrated auth: %v", err))
	}
	legacy, err := readAddGlobalUsersColumn(dbConn, "legacy_auth", "main")
	if err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("read legacy auth: %v", err))
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close post-migration database: %v", err)
	}
	if !addGlobalUsersJSONEqual(auth, expectedAuth) {
		return fmt.Sprintf("migrated auth got %s, want %s", auth, expectedAuth)
	}
	if !addGlobalUsersJSONEqual(legacy, legacyAuth) {
		return fmt.Sprintf("legacy auth got %s, want %s", legacy, legacyAuth)
	}
	return ""
}

func observeAddGlobalUsersRejected(pm *postmaster, teams map[string]string) string {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalUsersPreVersion)
	if err != nil {
		return fmt.Sprintf("open pre-migration database: %v", err)
	}
	for name, legacyAuth := range teams {
		if err := insertAddGlobalUsersTeam(dbConn, name, legacyAuth); err != nil {
			return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("insert legacy team %q: %v", name, err))
		}
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close pre-migration database: %v", err)
	}

	migrated, migrationErr := pm.runner.TryOpenDBAtVersion(addGlobalUsersPostVersion)
	if migrated != nil {
		if err := migrated.Close(); err != nil {
			return fmt.Sprintf("close unexpectedly migrated database: %v", err)
		}
	}
	if migrationErr == nil {
		return "migration succeeded; want rejection"
	}
	return ""
}

func observeAddGlobalUsersDownAllowed(pm *postmaster) string {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalUsersPostVersion)
	if err != nil {
		return fmt.Sprintf("open post-migration database: %v", err)
	}
	if err := insertAddGlobalUsersLegacyTeam(dbConn, "main", nil); err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("insert changed main team: %v", err))
	}
	legacy := `{"some-legacy-config":true}`
	if err := insertAddGlobalUsersLegacyTeam(dbConn, "another-team", &legacy); err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("insert unchanged non-main team: %v", err))
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close post-migration database: %v", err)
	}

	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalUsersPreVersion)
	if err != nil {
		return fmt.Sprintf("run add-global-users downgrade: %v", err)
	}
	auth, err := readAddGlobalUsersColumn(dbConn, "auth", "another-team")
	if err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("read downgraded auth: %v", err))
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close downgraded database: %v", err)
	}
	if !addGlobalUsersJSONEqual(auth, legacy) {
		return fmt.Sprintf("downgraded auth got %s, want %s", auth, legacy)
	}
	return ""
}

func observeAddGlobalUsersDownRejected(pm *postmaster) string {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalUsersPostVersion)
	if err != nil {
		return fmt.Sprintf("open post-migration database: %v", err)
	}
	if err := insertAddGlobalUsersLegacyTeam(dbConn, "some-team", nil); err != nil {
		return closeAddGlobalUsersDB(dbConn, fmt.Sprintf("insert changed non-main team: %v", err))
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close post-migration database: %v", err)
	}

	downgraded, migrationErr := pm.runner.TryOpenDBAtVersion(addGlobalUsersPreVersion)
	if downgraded != nil {
		if err := downgraded.Close(); err != nil {
			return fmt.Sprintf("close unexpectedly downgraded database: %v", err)
		}
	}
	if migrationErr == nil {
		return "downgrade succeeded; want rejection"
	}
	return ""
}

func insertAddGlobalUsersTeam(dbConn *sql.DB, name, auth string) error {
	result, err := dbConn.Exec("INSERT INTO teams(name, auth) VALUES($1, $2)", name, auth)
	if err != nil {
		return err
	}
	return requireAddGlobalUsersRow(result)
}

func insertAddGlobalUsersLegacyTeam(dbConn *sql.DB, name string, legacyAuth *string) error {
	result, err := dbConn.Exec("INSERT INTO teams(name, legacy_auth) VALUES($1, $2)", name, legacyAuth)
	if err != nil {
		return err
	}
	return requireAddGlobalUsersRow(result)
}

func requireAddGlobalUsersRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("insert affected %d rows, want 1", rows)
	}
	return nil
}

func readAddGlobalUsersColumn(dbConn *sql.DB, column, team string) (string, error) {
	query := "SELECT auth FROM teams WHERE name = $1"
	if column == "legacy_auth" {
		query = "SELECT legacy_auth FROM teams WHERE name = $1"
	} else if column != "auth" {
		return "", fmt.Errorf("unsupported team column %q", column)
	}
	var value string
	if err := dbConn.QueryRow(query, team).Scan(&value); err != nil {
		return "", err
	}
	return value, nil
}

func addGlobalUsersJSONEqual(actual, expected string) bool {
	var actualJSON any
	if err := json.Unmarshal([]byte(actual), &actualJSON); err != nil {
		return false
	}
	var expectedJSON any
	if err := json.Unmarshal([]byte(expected), &expectedJSON); err != nil {
		return false
	}
	return reflect.DeepEqual(actualJSON, expectedJSON)
}

func closeAddGlobalUsersDB(dbConn *sql.DB, failure string) string {
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("%s; close database: %v", failure, err)
	}
	return failure
}
