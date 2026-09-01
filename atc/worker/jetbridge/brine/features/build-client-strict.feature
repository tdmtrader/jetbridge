Feature: The production Go Concourse client and API manage real builds

  Source: 16 exact leaves in go-concourse/concourse/builds_test.go. Every
  scenario uses a real TCP listener, http.Server, production routes and access
  wrappers, PostgreSQL factories, and the production Go Concourse client.

  Scenario: CreateBuild sends the exact plan and decodes the persisted started build
    Given the production build boundary executes profile "client-create-one-off"
    Then the production build observation exactly matches profile "client-create-one-off"

  Scenario: CreateJobBuild sends the instanced pipeline query and decodes the pending build
    Given the production build boundary executes profile "client-create-job"
    Then the production build observation exactly matches profile "client-create-job"

  Scenario: JobBuild decodes the exact persisted job build
    Given the production build boundary executes profile "client-job-existing"
    Then the production build observation exactly matches profile "client-job-existing"

  Scenario: Build decodes the exact persisted global build
    Given the production build boundary executes profile "client-global-existing"
    Then the production build observation exactly matches profile "client-global-existing"

  Scenario: Global Builds applies the from cursor to real persisted rows
    Given the production build boundary executes profile "client-global-from"
    Then the production build observation exactly matches profile "client-global-from"

  Scenario: Global Builds applies the from cursor and limit to real persisted rows
    Given the production build boundary executes profile "client-global-from-limit"
    Then the production build observation exactly matches profile "client-global-from-limit"

  Scenario: Global Builds applies the to cursor to real persisted rows
    Given the production build boundary executes profile "client-global-to"
    Then the production build observation exactly matches profile "client-global-to"

  Scenario: Global Builds applies the to cursor and limit to real persisted rows
    Given the production build boundary executes profile "client-global-to-limit"
    Then the production build observation exactly matches profile "client-global-to-limit"

  Scenario: Global Builds applies both cursor bounds to real persisted rows
    Given the production build boundary executes profile "client-global-from-to"
    Then the production build observation exactly matches profile "client-global-from-to"

  Scenario: Global Builds returns nil pagination when production emits no Link header
    Given the production build boundary executes profile "client-global-pagination-empty"
    Then the production build observation exactly matches profile "client-global-pagination-empty"

  Scenario: Team Builds applies the from cursor to real persisted rows
    Given the production build boundary executes profile "client-team-from"
    Then the production build observation exactly matches profile "client-team-from"

  Scenario: Team Builds applies the from cursor and limit to real persisted rows
    Given the production build boundary executes profile "client-team-from-limit"
    Then the production build observation exactly matches profile "client-team-from-limit"

  Scenario: Team Builds applies the to cursor to real persisted rows
    Given the production build boundary executes profile "client-team-to"
    Then the production build observation exactly matches profile "client-team-to"

  Scenario: Team Builds applies the to cursor and limit to real persisted rows
    Given the production build boundary executes profile "client-team-to-limit"
    Then the production build observation exactly matches profile "client-team-to-limit"

  Scenario: Team Builds applies both cursor bounds to real persisted rows
    Given the production build boundary executes profile "client-team-from-to"
    Then the production build observation exactly matches profile "client-team-from-to"

  Scenario: Team Builds returns nil pagination when production emits no Link header
    Given the production build boundary executes profile "client-team-pagination-empty"
    Then the production build observation exactly matches profile "client-team-pagination-empty"
