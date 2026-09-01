Feature: Managing a pipeline through the real HTTP API

  The consumer here is fly and every other API client. Requests cross the real
  rata router and pipeline handlers and land in real PostgreSQL. The legacy
  atc/api/pipelines_test.go success paths wrap database objects in decorators
  that can record calls and inject errors. These scenarios keep only observable
  outcomes: the response and the row another web would load after the request.

  Source dispositions, counted by initial leaf spec:

    GET team pipelines: status, content type, persisted objects, complete list
      (4 specs at pipelines_test.go:1031-1048).
    GET one pipeline: status, content type and JSON object
      (3 specs at :1145-1156).
    Pause/unpause: status and durable state in both directions
      (4 specs at :1619-1623 and :1789-1793).
    Expose/hide: status and durable visibility in both directions
      (4 specs at :1879-1883 and :1967-1971).
    Archive, delete and order: the successful outcome of each operation
      (4 specs at :1531, :1709, :2081 and :2099).
    Rename: success plus the real not-found response
      (2 specs at :2435 and :2456).
    Create a pipeline build: status, content type, started row and response
      (4 specs at :2942-2967).

  This batch therefore moves 25 initial specs. Error injection, authorization,
  badge rendering, pagination and versions-db shapes stay in Go; none is
  silently claimed by these scenarios.

  Scenario: Listing pipelines returns the persisted collection as JSON
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "alpha,beta,gamma"
    And pipeline "alpha" is exposed in PostgreSQL
    And pipeline "beta" is paused by "automatic-pipeline-archiver" in PostgreSQL
    When the API lists the team's pipelines
    Then the API response status is 200
    And the API response content type contains "application/json"
    And the API returned the pipelines "alpha,beta,gamma"

  Scenario: Reading one pipeline returns its API representation
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "alpha"
    And pipeline "alpha" is exposed in PostgreSQL
    When the API reads pipeline "alpha"
    Then the API response status is 200
    And the API response content type contains "application/json"
    And the API returned pipeline "alpha"

  Scenario: Pausing and unpausing a pipeline survives a fresh database read
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "deploy"
    When the API pauses pipeline "deploy"
    Then the API response status is 200
    And pipeline "deploy" is paused by the API user
    When the API unpauses pipeline "deploy"
    Then the API response status is 200
    And pipeline "deploy" is unpaused

  Scenario: Exposing and hiding a pipeline changes what anonymous clients may see
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "docs"
    When the API exposes pipeline "docs"
    Then the API response status is 200
    And pipeline "docs" is public
    When the API hides pipeline "docs"
    Then the API response status is 200
    And pipeline "docs" is private

  Scenario: Archiving a pipeline persists the archived state
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "retired"
    When the API archives pipeline "retired"
    Then the API response status is 200
    And pipeline "retired" is archived

  Scenario: Deleting a pipeline removes it from PostgreSQL
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "temporary"
    When the API deletes pipeline "temporary"
    Then the API response status is 204
    And pipeline "temporary" no longer exists

  Scenario: Ordering pipelines persists the requested order
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "alpha,beta,gamma"
    When the API orders the pipelines as "gamma,alpha,beta"
    Then the API response status is 200
    And the persisted pipeline order is "gamma,alpha,beta"

  Scenario: Renaming addresses the named pipeline and reports a missing one
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "old-name"
    When the API renames pipeline "old-name" to "new-name"
    Then the API response status is 200
    And pipeline "old-name" no longer exists
    And pipeline "new-name" exists
    When the API renames pipeline "absent" to "anything"
    Then the API response status is 404

  Scenario: Creating a pipeline build returns and persists a started build
    Given the real pipeline API and PostgreSQL
    And the team has the pipelines "manual"
    When the API starts a build for pipeline "manual"
    Then the API response status is 201
    And the API response content type contains "application/json"
    And pipeline "manual" has one started build
    And the API returned a started build for pipeline "manual"
