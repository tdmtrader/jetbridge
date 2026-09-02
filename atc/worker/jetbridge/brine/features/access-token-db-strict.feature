Feature: Access-token persistence and expiry over real PostgreSQL

  Scenario Outline: Production access-token database behavior — <profile>
    Given the production access-token database profile "<profile>" is exercised
    Then the access-token database observation exactly matches "<profile>"

    Examples:
      | profile                    |
      | create-and-fetch-claims    |
      | delete-token               |
      | remove-expired-keeps-active |
      | expiration-leeway          |
