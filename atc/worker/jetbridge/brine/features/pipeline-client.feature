Feature: The Go Concourse client manages pipelines through the real API

  Source: 14 exact specs in go-concourse/concourse/pipelines_test.go. The
  replacement crosses the production client, rata router, handlers, accessor,
  and PostgreSQL over a real TCP listener. The five admissible API specs are
  isolated in pipeline-client-api-strict.feature; the injected-error-only API
  leaf remains in Go. Ten client leaves whose assertions require request
  recording or an injected error remain in Go.

  Scenario: Existing instanced pipeline supports pauses
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "plain"
    When the Go client "pauses" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "paused"

  Scenario: Existing instanced pipeline supports archives
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "plain"
    When the Go client "archives" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "archived"

  Scenario: Existing instanced pipeline supports unpauses
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "paused"
    When the Go client "unpauses" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "unpaused"

  Scenario: Existing instanced pipeline supports exposes
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "plain"
    When the Go client "exposes" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "public"

  Scenario: Existing instanced pipeline supports hides
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "public"
    When the Go client "hides" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "private"

  Scenario: Pipeline lookup decodes a real instanced pipeline
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "paused"
    When the Go client reads instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And the client decoded the exact persisted pipeline "target"

  Scenario: Pipeline listing scope team decodes real objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    And public pipeline "outside" exists on client team "other-team"
    When the Go client lists "team" pipelines
    Then the client decoded exact persisted pipelines "alpha,beta"
    And the Go client returned no error

  Scenario: Pipeline listing scope all decodes real objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    And public pipeline "outside" exists on client team "other-team"
    When the Go client lists "all" pipelines
    Then the client decoded exact persisted pipelines "alpha,beta,other-team/outside"
    And the Go client returned no error

  Scenario: Ordering pipelines sends a body accepted and persisted by the API
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client orders named pipelines as "beta,alpha"
    Then the Go client returned no error
    And the persisted named pipeline order is "beta,alpha"

  Scenario: Rename returns the real API result
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "old"
    When the Go client renames pipeline "old" to "new"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "old" was renamed to "new" in PostgreSQL

  Scenario: Rename of a missing pipeline is not an error
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client renames pipeline "missing" to "new"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Creating a pipeline build returns the exact persisted build
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    When the Go client creates a build for instanced pipeline "target"
    Then the client returned the exact persisted created build
    And the Go client returned no error

  Scenario: Listing pipeline builds returns the exact persisted builds
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" has two persisted builds
    When the Go client lists builds for instanced pipeline "target"
    Then the Go client found the resource
    And the client returned the exact two persisted builds
    And the Go client returned no error

  Scenario: Listing pipeline builds without Link headers returns nil pagination
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" has two persisted builds
    When the Go client lists builds for instanced pipeline "target"
    Then the Go client found the resource
    And the client returned nil pipeline-build pagination
    And the Go client returned no error
