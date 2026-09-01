Feature: The Go Concourse client manages builds through the real API

  Source: 33 initial specs. The original 21 are in
  go-concourse/concourse/builds_test.go and cover one-off/job creation, lookup,
  abort, cursor combinations, and empty pagination. The real API path also
  replaces 12 handler specs: one-off create status/state/body
  (atc/api/builds_test.go:641,652,671), global-list status/body (:919/:930),
  exact-build status/body and missing (:1251/:1262/:1174), abort success
  (:1947), manual job creation (atc/api/jobs_test.go:1474), and exact/missing
  job-build lookup (:1860/:1878). Requests use production serialization,
  routes, handlers, database models, and PostgreSQL rather than canned HTTP.

  Scenario: Creating a one-off build returns its real started representation
    Given the production Go build client, real API, and PostgreSQL
    When the Go client creates a one-off build
    Then the Go build client returned no error
    And the Go build client state is "id-positive=true;status=started"

  Scenario: Creating a manual job build returns its persisted representation
    Given the production Go build client, real API, and PostgreSQL
    And the build client team has a real instanced job
    When the Go client creates a job build
    Then the Go build client returned no error
    And the Go build client state is "id-positive=true;status=pending"

  Scenario Outline: Existing <kind> build lookup decodes the persisted build
    Given the production Go build client, real API, and PostgreSQL
    And the build client team has a real instanced job
    And the build client has a persisted "<fixture>" build
    When the Go client reads the "<kind>" build
    Then the Go build client found the resource
    And the Go build client returned no error

    Examples:
      | kind   | fixture |
      | global | one-off |
      | job    | job     |

  Scenario Outline: Missing <kind> build lookup is not an error
    Given the production Go build client, real API, and PostgreSQL
    And the build client team has a real instanced job
    When the Go client reads a missing "<kind>" build
    Then the Go build client did not find the resource
    And the Go build client returned no error

    Examples:
      | kind   |
      | global |
      | job    |

  Scenario Outline: <scope> build listing decodes all builds and nil cursors
    Given the production Go build client, real API, and PostgreSQL
    And the build client has 3 persisted one-off builds
    When the Go client lists "<scope>" builds with page "all"
    Then the Go build client returned 3 build(s)
    And the Go build client returned empty pagination
    And the Go build client returned no error

    Examples:
      | scope  |
      | global |
      | team   |

  Scenario Outline: <scope> build listing accepts the <page> cursor combination
    Given the production Go build client, real API, and PostgreSQL
    And the build client has 3 persisted one-off builds
    When the Go client lists "<scope>" builds with page "<page>"
    Then the Go build client returned no error

    Examples:
      | scope  | page       |
      | global | from       |
      | global | from-limit |
      | global | to         |
      | global | to-limit   |
      | global | from-to    |
      | team   | from       |
      | team   | from-limit |
      | team   | to         |
      | team   | to-limit   |
      | team   | from-to    |

  Scenario: Aborting a running build persists the aborted flag
    Given the production Go build client, real API, and PostgreSQL
    And the build client has a persisted "one-off" build
    When the Go client aborts the persisted build
    Then the Go build client returned no error
    And the Go build client state is "aborted=true"
