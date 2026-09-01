Feature: API authentication boundaries expose only authorized handlers

  Source: all 18 specs in check_admin_handler_test.go,
  check_authentication_handler_test.go, and check_authorization_handler_test.go.
  Tokens are real access_tokens rows, roles are stored on real teams, access is
  constructed by the production verifier/factory, and assertions observe the
  HTTP status and whether the delegate's response crossed the boundary.

  Scenario Outline: The auth boundary answers from persisted identity — <boundary>/<identity>
    Given the real API auth boundary "<boundary>" receives identity "<identity>"
    Then the auth response status is <status>
    And the auth response body is "<body>"

    Examples:
      | boundary                   | identity    | status | body           |
      | admin                      | admin       | 200    | delegate       |
      | admin                      | team-owner  | 403    | forbidden      |
      | admin                      | anonymous   | 401    | not authorized |
      | authentication             | valid       | 200    | delegate       |
      | authentication             | anonymous   | 401    | not authorized |
      | authentication-if-provided | expired     | 401    | not authorized |
      | authentication-if-provided | valid       | 200    | delegate       |
      | authentication-if-provided | anonymous   | 200    | delegate       |
      | team-authorization         | same-team   | 200    | delegate       |
      | team-authorization         | other-team  | 403    | forbidden      |
      | team-authorization         | anonymous   | 401    | not authorized |
