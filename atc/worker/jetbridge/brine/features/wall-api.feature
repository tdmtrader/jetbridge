Feature: Cluster wall messages round-trip through the Go client and API

  Source: 10 success/validation specs in atc/api/wall_test.go, all 3 specs
  in go-concourse/concourse/wall_test.go, and all 9 specs in atc/db/wall_test.go,
  for 22 specs over real PostgreSQL. The API/client scenarios also cover seven
  database wall specs through the same object. The two final scenarios use a
  fixed production Clock to make expiry and replacement deterministic.

  Scenario: A permanent wall message returns JSON over the API
    When the real wall API handles profile "get-message"
    Then the wall API returned status 200
    And the wall API content type is "application/json"
    And the stored wall message is "test message"

  Scenario: An expiring wall message returns its remaining TTL
    When the real wall API handles profile "get-expiring"
    Then the stored wall message is "test message"
    And the wall TTL is close to one minute

  Scenario: The Go client decodes the wall response
    When the real wall API handles profile "client-get"
    Then the stored wall message is "test message"
    And the wall TTL is close to one minute
    And the wall client returned no error

  Scenario: The Go client sets and persists a wall message
    When the real wall API handles profile "client-set"
    Then the stored wall message is "set message"
    And the wall TTL is close to one minute
    And the wall client returned no error

  Scenario: An empty wall message is rejected and stores nothing
    When the real wall API handles profile "invalid-empty"
    Then the wall API returned status 400
    And the stored wall message is ""
    And the wall client returned no error

  Scenario: The Go client clears the persisted wall
    When the real wall API handles profile "client-clear"
    Then the stored wall message is ""
    And the wall client returned no error

  Scenario: An expired database wall is absent at the clock that reads it
    When the real wall API handles profile "db-expired"
    Then the stored wall message is ""
    And the wall client returned no error

  Scenario: Replacing a database wall leaves only the last message
    When the real wall API handles profile "db-last"
    Then the stored wall message is "third"
    And the wall client returned no error
