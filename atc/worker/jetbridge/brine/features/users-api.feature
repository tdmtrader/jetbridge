Feature: Administrators inspect current and recently active users

  Source: 12 success/filter/validation specs in atc/api/users_test.go. Requests
  cross production auth routing and real PostgreSQL user rows.

  Scenario: Current user returns the authenticated production accessor
    When the real users API handles profile "current"
    Then the users API returned status 200
    And the users API content type is "application/json"
    And the users response contains "brine-user"
    And the users response contains "is_admin"

  Scenario: Listing an empty user table returns an empty JSON array
    When the real users API handles profile "list-empty"
    Then the users API returned status 200
    And the users API content type is "application/json"
    And the users API returned users ""

  Scenario: Listing users returns persisted login metadata
    When the real users API handles profile "list-user"
    Then the users API returned users "bob"

  Scenario: A past since date includes a recent login
    When the real users API handles profile "since-past"
    Then the users API returned status 200
    And the users API returned users "bob"

  Scenario: A future since date excludes every login
    When the real users API handles profile "since-future"
    Then the users API returned users ""

  Scenario: An invalid since date returns JSON validation and bad request
    When the real users API handles profile "since-invalid"
    Then the users API returned status 400
    And the users response contains "wrong date format"

  Scenario: An empty since value returns no users
    When the real users API handles profile "since-empty"
    Then the users API returned users ""
