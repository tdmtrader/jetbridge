Feature: Production users API serialization and filtering

  Every request crosses a real TCP listener, the selected production rata
  route, the production verifier/accessor/auditor and users handler, with
  identity, administrator teams, and user login rows persisted in PostgreSQL.

  Scenario: The current-user endpoint succeeds for a persisted identity
    When the production users API handles profile "current"
    Then the users API returned status 200

  Scenario: The current-user endpoint returns the production JSON content type
    When the production users API handles profile "current"
    Then the users API content type is "application/json"

  Scenario: The current-user endpoint serializes every persisted identity field
    When the production users API handles profile "current"
    Then the users API returns the exact persisted identity

  Scenario: The administrator users endpoint succeeds with no login rows
    When the production users API handles profile "list-empty"
    Then the users API returned status 200

  Scenario: The administrator users endpoint returns the production JSON content type
    When the production users API handles profile "list-empty"
    Then the users API content type is "application/json"

  Scenario: The administrator users endpoint serializes no rows as an empty array
    When the production users API handles profile "list-empty"
    Then the users API returns the exact empty JSON array

  Scenario: The administrator users endpoint returns persisted login metadata
    When the production users API handles profile "list-user"
    Then the users API returns the exact persisted user metadata

  Scenario: A past since date returns persisted login metadata
    When the production users API handles profile "since-past"
    Then the users API returns the exact persisted user metadata

  Scenario: A future since date returns an empty array
    When the production users API handles profile "since-future"
    Then the users API returns the exact empty JSON array

  Scenario: An invalid since date returns the exact production validation document
    When the production users API handles profile "since-invalid"
    Then the users API returns the exact invalid-date document

  Scenario: An invalid since date returns bad request
    When the production users API handles profile "since-invalid"
    Then the users API returned status 400

  Scenario: An empty since value returns an empty array
    When the production users API handles profile "since-empty"
    Then the users API returns the exact empty JSON array
