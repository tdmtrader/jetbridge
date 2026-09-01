Feature: The production job-build API manages real job builds

  Source: 4 exact leaves in atc/api/jobs_test.go. Every scenario uses a real
  TCP listener, http.Server, production routes and access wrappers, PostgreSQL
  factories, and real response, check-build, and database state.

  Scenario: The manual-job API persists one pending build and schedules a real pinned check
    Given the production build boundary executes profile "api-create-job"
    Then the production build observation exactly matches profile "api-create-job"

  Scenario: The authorized exact-job-build API returns JSON content type and the exact persisted build
    Given the production build boundary executes profile "api-job-existing"
    Then the production build observation exactly matches profile "api-job-existing"

  Scenario: The public exact-job-build API returns the exact persisted build to an authenticated outsider
    Given the production build boundary executes profile "api-job-public-existing"
    Then the production build observation exactly matches profile "api-job-public-existing"

  Scenario: The exact-job-build API returns Not Found for a naturally absent build
    Given the production build boundary executes profile "api-job-missing"
    Then the production build observation exactly matches profile "api-job-missing"
