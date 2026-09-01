Feature: The Go Concourse client manages pipelines through the real API

  Source: 30 initial specs. The original 24 are in
  go-concourse/concourse/pipelines_test.go. Six additional, non-overlapping
  atc/api/pipelines_test.go specs are covered by the same real requests: global
  list status/body (:926/:937), missing named ordering (:2115), pipeline-build
  body and missing response (:2735/:2835), and created pipeline-build response
  (:2967). Success paths already owned by api-pipelines.feature are not counted
  again. Requests cross the production client, rata router, handlers, and
  PostgreSQL.

  Scenario Outline: Existing instanced pipeline supports <operation>
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    When the Go client "<operation>" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error

    Examples:
      | operation |
      | pauses    |
      | archives  |
      | unpauses  |
      | exposes   |
      | hides     |
      | deletes   |

  Scenario Outline: Missing instanced pipeline reports not found for <operation>
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "<operation>" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

    Examples:
      | operation |
      | pauses    |
      | archives  |
      | unpauses  |
      | exposes   |
      | hides     |
      | deletes   |

  Scenario: Pipeline lookup decodes a real instanced pipeline
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    When the Go client reads instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned pipelines "target"

  Scenario: Missing pipeline lookup returns false without an error
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client reads instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario Outline: Pipeline listing scope <scope> decodes real objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client lists "<scope>" pipelines
    Then the Go client returned pipelines "alpha,beta"
    And the Go client returned no error

    Examples:
      | scope |
      | team  |
      | all   |

  Scenario: Ordering pipelines sends a body accepted and persisted by the API
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client orders named pipelines as "beta,alpha"
    Then the Go client returned no error

  Scenario: Ordering a missing pipeline propagates the API error
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha"
    When the Go client orders named pipelines as "alpha,missing"
    Then the Go client returned an error

  Scenario: Rename returns the real API result
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "old"
    When the Go client renames pipeline "old" to "new"
    Then the Go client found the resource
    And the Go client returned 0 warning(s)
    And the Go client returned no error

  Scenario: Rename of a missing pipeline is not an error
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client renames pipeline "missing" to "new"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Creating and listing a pipeline build round-trips through PostgreSQL
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    When the Go client creates a build for instanced pipeline "target"
    Then the Go client returned a created build
    And the Go client returned no error
    When the Go client lists builds for instanced pipeline "target"
    Then the Go client found the resource
    And the Go client observed 1 build(s)
    And the Go client returned empty pagination

  Scenario: Listing builds for a missing pipeline returns not found
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client lists builds for instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error
