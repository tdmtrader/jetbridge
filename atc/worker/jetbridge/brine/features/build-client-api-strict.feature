Feature: The production build API manages real builds

  Source: 9 exact leaves in atc/api/builds_test.go. Every scenario uses a real
  TCP listener, http.Server, production routes and access wrappers, PostgreSQL
  factories, and real response and database state.

  Scenario: The create-build API returns Created for a real started build
    Given the production build boundary executes profile "api-create-status"
    Then the production build observation exactly matches profile "api-create-status"

  Scenario: The create-build API returns the production JSON content type
    Given the production build boundary executes profile "api-create-content-type"
    Then the production build observation exactly matches profile "api-create-content-type"

  Scenario: The create-build API persists the exact plan and started state
    Given the production build boundary executes profile "api-create-state"
    Then the production build observation exactly matches profile "api-create-state"

  Scenario: The create-build API returns the exact persisted build body
    Given the production build boundary executes profile "api-create-body"
    Then the production build observation exactly matches profile "api-create-body"

  Scenario: The global-build API returns OK for visible persisted builds
    Given the production build boundary executes profile "api-list-status"
    Then the production build observation exactly matches profile "api-list-status"

  Scenario: The authenticated global-build API returns OK for visible persisted builds
    Given the production build boundary executes profile "api-list-auth-status"
    Then the production build observation exactly matches profile "api-list-auth-status"

  Scenario: The exact-build API returns the production JSON content type
    Given the production build boundary executes profile "api-get-content-type"
    Then the production build observation exactly matches profile "api-get-content-type"

  Scenario: The exact-build API returns the exact persisted build body
    Given the production build boundary executes profile "api-get-body"
    Then the production build observation exactly matches profile "api-get-body"

  Scenario: The abort-build API persists abort and returns No Content
    Given the production build boundary executes profile "api-abort"
    Then the production build observation exactly matches profile "api-abort"
