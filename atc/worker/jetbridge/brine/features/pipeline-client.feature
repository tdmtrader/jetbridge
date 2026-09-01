Feature: The Go Concourse client manages pipelines through the real API

  Source: 24 exact specs in go-concourse/concourse/pipelines_test.go. The
  replacement crosses the production client, rata router, handlers, accessor,
  and PostgreSQL over a real TCP listener. The five admissible API specs are
  isolated in pipeline-client-api-strict.feature; the injected-error-only API
  leaf remains in Go.

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

  Scenario: Existing instanced pipeline supports deletes
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "plain"
    When the Go client "deletes" instanced pipeline "target"
    Then the Go client found the resource
    And the Go client returned no error
    And pipeline "target" persisted state is "deleted"

  Scenario: Missing instanced pipeline reports not found for pauses
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "pauses" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Missing instanced pipeline reports not found for archives
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "archives" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Missing instanced pipeline reports not found for unpauses
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "unpauses" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Missing instanced pipeline reports not found for exposes
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "exposes" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Missing instanced pipeline reports not found for hides
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "hides" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Missing instanced pipeline reports not found for deletes
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client "deletes" instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Pipeline lookup decodes a real instanced pipeline
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has instanced pipeline "target"
    And the client instanced pipeline "target" starts "paused"
    When the Go client reads instanced pipeline "target"
    Then the Go client found the resource
    And the client decoded the exact persisted pipeline "target"

  Scenario: Missing pipeline lookup returns false without an error
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client reads instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error

  Scenario: Pipeline listing scope team decodes real objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client lists "team" pipelines
    Then the client decoded exact persisted pipelines "alpha,beta"
    And the Go client returned no error

  Scenario: Pipeline listing scope all decodes real objects
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client lists "all" pipelines
    Then the client decoded exact persisted pipelines "alpha,beta"
    And the Go client returned no error

  Scenario: Ordering pipelines sends a body accepted and persisted by the API
    Given the production Go pipeline client, real API, and PostgreSQL
    And the client team has named pipelines "alpha,beta"
    When the Go client orders named pipelines as "beta,alpha"
    Then the Go client returned no error
    And the persisted named pipeline order is "beta,alpha"

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
    Then the client returned nil pipeline-build pagination
    And the Go client returned no error

  Scenario: Listing builds for a missing pipeline returns not found
    Given the production Go pipeline client, real API, and PostgreSQL
    When the Go client lists builds for instanced pipeline "missing"
    Then the Go client did not find the resource
    And the Go client returned no error
