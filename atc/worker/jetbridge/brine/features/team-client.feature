Feature: Team administration crosses the production client and API

  Source: 8 specs in go-concourse/concourse/teams_test.go and 7 persisted
  success/not-found specs in atc/api/teams_test.go. The same requests exercise
  client decoding, auth routing, handlers, and PostgreSQL, for 15 specs total.

  Scenario: Team listing returns only real authorized teams
    Given the production Go team client, real API, and PostgreSQL
    And the team API has persisted teams "alpha,beta"
    When the Go client lists teams
    Then the Go team client returned teams "alpha,api-team,beta"
    And the Go team client returned no error

  Scenario: Finding a persisted team returns the production representation
    Given the production Go team client, real API, and PostgreSQL
    When the Go client finds team "api-team"
    Then the Go team client found the team
    And the Go team client returned teams "api-team"
    And the Go team client returned no error

  Scenario: Finding a missing team returns the client's descriptive error
    Given the production Go team client, real API, and PostgreSQL
    When the Go client finds team "missing"
    Then the Go team client returned an error

  Scenario: Creating a team reports created and persists it
    Given the production Go team client, real API, and PostgreSQL
    When the Go client "creates" team "target"
    Then the Go team client save result is "created=true;updated=false;warnings=0"
    And the Go team client returned teams "target"
    And the Go team client returned no error

  Scenario: Updating a team reports updated
    Given the production Go team client, real API, and PostgreSQL
    When the Go client "updates" team "target"
    Then the Go team client save result is "created=false;updated=true;warnings=0"
    And the Go team client returned no error

  Scenario: Compatibility-invalid team names return a warning and persist
    Given the production Go team client, real API, and PostgreSQL
    When the Go client creates warning-named team
    Then the Go team client save result is "created=true;updated=false;warnings=1"
    And the Go team client returned no error

  Scenario: Destroying a team removes its persisted row
    Given the production Go team client, real API, and PostgreSQL
    And the team API has persisted teams "target"
    When the Go client destroys team "target"
    Then the persisted team is absent
    And the Go team client returned no error

  Scenario: Deleting a missing team returns not found
    Given the production Go team client, real API, and PostgreSQL
    When the team API deletes missing team
    Then the team API returned status 404
