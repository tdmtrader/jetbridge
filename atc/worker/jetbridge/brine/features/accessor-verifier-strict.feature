Feature: Access token verification over real PostgreSQL

  Scenario Outline: Production access-token behavior — <profile>
    Given the production access-token profile "<profile>" is exercised
    Then the access-token observation exactly matches "<profile>"

    Examples:
      | profile                 |
      | no-token                |
      | invalid-header          |
      | invalid-token-type      |
      | token-not-found         |
      | expired-token           |
      | invalid-audience        |
      | valid-token-succeeds    |
      | valid-token-claims      |
      | deleted-token-not-found |
