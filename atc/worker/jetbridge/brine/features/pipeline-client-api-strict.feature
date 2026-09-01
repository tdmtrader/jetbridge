Feature: Pipeline client API responses cross production routes and PostgreSQL

  Source: five exact leaves in atc/api/pipelines_test.go: global pipeline list
  status and body, missing named ordering status, pipeline-build body, and
  created pipeline-build response. The injected pipeline.Builds error leaf is
  deliberately retained because real PostgreSQL cannot produce that error and
  a missing-pipeline 404 traverses a different production path.

  Scenario: Global pipeline listing returns status 200
    Given the production Go pipeline client, real API, and PostgreSQL
    And public pipeline "public-main" exists on client team "api-team"
    And public pipeline "public-other" exists on client team "other-team"
    When the raw production pipeline API performs "lists all pipelines"
    Then the raw pipeline API status is 200

  Scenario: Global pipeline listing returns exact public pipeline objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And public pipeline "public-main" exists on client team "api-team"
    And public pipeline "public-other" exists on client team "other-team"
    When the raw production pipeline API performs "lists all pipelines"
    Then the raw pipeline API returned both exact public pipelines

  Scenario: Ordering a missing named pipeline returns the exact bad request
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha"
    When the raw production pipeline API performs "orders a missing pipeline"
    Then the raw pipeline API status is 400
    And the raw pipeline API returned the exact missing-order error

  Scenario: Pipeline-build listing returns exact persisted build objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" has two persisted builds
    When the raw production pipeline API performs "lists pipeline builds"
    Then the raw pipeline API returned the exact persisted builds

  Scenario: Pipeline-build creation returns the exact persisted build object
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    When the raw production pipeline API performs "creates a pipeline build"
    Then the raw pipeline API returned the exact created build
