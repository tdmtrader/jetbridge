Feature: Production user factory persistence

  Scenario: Creating a new user preserves its name and current login time
    When the production user factory evaluates profile "new-user"
    Then the user factory observation exactly matches "new-user"

  Scenario: The same username through different connectors creates distinct users
    When the production user factory evaluates profile "different-connector"
    Then the user factory observation exactly matches "different-connector"

  Scenario: Repeating the same subject keeps one user
    When the production user factory evaluates profile "same-subject-count"
    Then the user factory observation exactly matches "same-subject-count"

  Scenario: Repeating the same subject preserves its exact username
    When the production user factory evaluates profile "same-subject-name"
    Then the user factory observation exactly matches "same-subject-name"

  Scenario: Repeating the same subject updates its login time
    When the production user factory evaluates profile "same-subject-login"
    Then the user factory observation exactly matches "same-subject-login"
