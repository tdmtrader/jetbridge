Feature: ID token manager configuration over real PostgreSQL

  Scenario Outline: Production ID token manager behavior — <profile>
    Given the production ID token manager profile "<profile>" is exercised
    Then the ID token manager observation exactly matches "<profile>"

    Examples:
      | profile                 |
      | valid                   |
      | malformed-audience      |
      | malformed-subject-scope |
      | malformed-expires-in    |
      | malformed-algorithm     |
      | unknown-setting         |
      | unknown-subject-scope   |
      | excessive-expires-in    |
      | unknown-algorithm       |
