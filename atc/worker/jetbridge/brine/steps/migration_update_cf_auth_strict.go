package steps

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

const (
	updateCFAuthPreMigration  = 1569945021
	updateCFAuthPostMigration = 1572899256
)

type MigrationUpdateCFAuthObservation struct {
	Profile string
	Failure string
}

type updateCFAuthProfile struct {
	direction string
	before    string
	after     string
}

var updateCFAuthProfiles = map[string]updateCFAuthProfile{
	"up-org-space": {
		direction: "up",
		before:    `{"owner":{"groups":["cf:test-org:space1","cf:test-org"],"users":["local:test"]}}`,
		after:     `{"owner":{"groups":["cf:test-org:space1:developer","cf:test-org"],"users":["local:test"]}}`,
	},
	"up-cf-combinations": {
		direction: "up",
		before:    `{"owner":{"groups":["cf","cf:cf","cf:cf:cf","cf:cf:cf:cf","cf:cf:cf:cf:cf"],"users":["local:cf"]}}`,
		after:     `{"owner":{"groups":["cf","cf:cf","cf:cf:cf:developer","cf:cf:cf:cf:developer","cf:cf:cf:cf:cf:developer"],"users":["local:cf"]}}`,
	},
	"up-github-groups": {
		direction: "up",
		before:    `{"owner":{"groups":["github:pivotal-cf","github:pivotal-cf:cf","github:pivotal-cf:cf:cf","github:pivotal-cf:cf:cf:cf:cf","github:pivotal-cf:cf:cf:cf:cf:cf"],"users":["local:cf"]}}`,
		after:     `{"owner":{"groups":["github:pivotal-cf","github:pivotal-cf:cf","github:pivotal-cf:cf:cf","github:pivotal-cf:cf:cf:cf:cf","github:pivotal-cf:cf:cf:cf:cf:cf"],"users":["local:cf"]}}`,
	},
	"down-developer": {
		direction: "down",
		before:    `{"owner":{"groups":["cf:test-org:space1:developer","cf:test-org"],"users":["local:test"]}}`,
		after:     `{"owner":{"groups":["cf:test-org:space1","cf:test-org"],"users":["local:test"]}}`,
	},
	"down-other-roles": {
		direction: "down",
		before:    `{"owner":{"groups":["cf:test-org:space1:auditor","cf:test-org:space1:manager"],"users":["local:test"]}}`,
		after:     `{"owner":{"groups":["cf:test-org:space1:auditor","cf:test-org:space1:manager"],"users":["local:test"]}}`,
	},
}

func MigrationUpdateCFAuthStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MigrationUpdateCFAuthObservation](
			"the actual Cloud Foundry authorization migration evaluates profile {string}",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (MigrationUpdateCFAuthObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return MigrationUpdateCFAuthObservation{}, fmt.Errorf("expected Cloud Foundry authorization profile")
				}
				postgres, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return MigrationUpdateCFAuthObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return MigrationUpdateCFAuthObservation{
					Profile: profile,
					Failure: observeMigrationUpdateCFAuth(postgres, profile),
				}, nil
			},
		),
		brine.DefineCheck[MigrationUpdateCFAuthObservation](
			"the Cloud Foundry authorization migration exactly matches {string}",
			func(observation MigrationUpdateCFAuthObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected Cloud Foundry authorization profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("Cloud Foundry authorization migration observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeMigrationUpdateCFAuth(postgres *postmaster, profileName string) string {
	profile, found := updateCFAuthProfiles[profileName]
	if !found {
		return fmt.Sprintf("unknown profile %q", profileName)
	}

	runner := postgres.runner
	runner.CreateEmptyTestDB()
	defer runner.DropTestDB()

	startVersion := updateCFAuthPreMigration
	endVersion := updateCFAuthPostMigration
	if profile.direction == "down" {
		startVersion, endVersion = endVersion, startVersion
	}

	database, err := runner.TryOpenDBAtVersion(startVersion)
	if err != nil {
		return fmt.Sprintf("open at start version: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO teams (name, auth) VALUES ('main', $1)`, profile.before); err != nil {
		database.Close()
		return fmt.Sprintf("insert team: %v", err)
	}
	if err := database.Close(); err != nil {
		return fmt.Sprintf("close start-version database: %v", err)
	}

	database, err = runner.TryOpenDBAtVersion(endVersion)
	if err != nil {
		return fmt.Sprintf("open at end version: %v", err)
	}
	defer database.Close()

	var actualJSON []byte
	if err := database.QueryRow(`SELECT auth FROM teams WHERE name = 'main'`).Scan(&actualJSON); err != nil {
		return fmt.Sprintf("read migrated auth: %v", err)
	}
	var actual, expected any
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		return fmt.Sprintf("decode migrated auth %q: %v", actualJSON, err)
	}
	if err := json.Unmarshal([]byte(profile.after), &expected); err != nil {
		return fmt.Sprintf("decode expected auth: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Sprintf("auth got %s, want %s", strings.TrimSpace(string(actualJSON)), profile.after)
	}
	return ""
}
