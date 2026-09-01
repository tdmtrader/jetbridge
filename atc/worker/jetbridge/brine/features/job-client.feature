Feature: The Go Concourse client manages jobs through the real API

  Source: 26 initial specs. The original 17 are in
  go-concourse/concourse/jobs_test.go and cover pipeline/global listing,
  lookup, cursor combinations, missing resources, and the three job state
  mutations. Because every request crosses the production router and handlers,
  the same scenarios also replace these 9 distinct atc/api/jobs_test.go specs:
  administrator global listing (:547), missing lookup (:720), missing build
  listing (:1439), and existing/missing pause (:2131/:2148), unpause
  (:2193/:2210), and schedule (:2384/:2399). PostgreSQL models and real
  instance-vars query encoding participate throughout.

  Scenario Outline: Job listing scope <scope> returns the persisted job
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client lists "<scope>" jobs
    Then the Go job client returned jobs "build"
    And the Go job client returned no error

    Examples:
      | scope    |
      | pipeline |
      | all      |

  Scenario: Reading an existing job decodes its production representation
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client reads job "build"
    Then the Go job client found the resource
    And the Go job client returned jobs "build"
    And the Go job client returned no error

  Scenario: Reading a missing job returns not found without an error
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client reads job "missing"
    Then the Go job client did not find the resource
    And the Go job client returned no error

  Scenario: Listing all persisted job builds returns real builds and nil cursors
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    And the real job has 3 persisted builds
    When the Go client lists job builds with page "all"
    Then the Go job client found the resource
    And the Go job client returned 3 build(s)
    And the Go job client returned empty pagination
    And the Go job client returned no error

  Scenario Outline: Job build page <page> is accepted by the production API
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    And the real job has 3 persisted builds
    When the Go client lists job builds with page "<page>"
    Then the Go job client found the resource
    And the Go job client returned no error

    Examples:
      | page       |
      | from       |
      | from-limit |
      | to         |
      | to-limit   |
      | from-to    |

  Scenario: Listing builds for a missing job returns not found
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client lists builds for missing job
    Then the Go job client did not find the resource
    And the Go job client returned no error

  Scenario Outline: Existing job mutation <operation> persists <state>
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client "<operation>" job "build"
    Then the Go job client found the resource
    And the Go job client returned no error
    And the persisted job state is "<state>"

    Examples:
      | operation | state         |
      | pauses    | paused=true   |
      | unpauses  | paused=false  |
      | schedules | advanced=true |

  Scenario Outline: Missing job mutation <operation> returns not found
    Given the production Go job client, real API, and PostgreSQL
    And the client team has a real instanced job
    When the Go client "<operation>" job "missing"
    Then the Go job client did not find the resource
    And the Go job client returned no error

    Examples:
      | operation |
      | pauses    |
      | unpauses  |
      | schedules |
