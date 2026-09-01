Feature: Production API authentication and authorization boundaries

  The requests cross a real TCP listener, the selected production rata route,
  the production accessor/verifier/auditor, and a concrete production API
  handler backed by PostgreSQL. Access tokens, teams, roles, and the pipeline
  returned by the authorized endpoint are persisted in the scenario database.

  Scenario: An administrator request receives an OK status
    Given the real API auth boundary "admin" receives identity "admin"
    Then the auth response status is 200

  Scenario: An administrator reaches the production active-users endpoint
    Given the real API auth boundary "admin" receives identity "admin"
    Then the auth response is the exact empty active-users document
    And the auth response content type is "application/json"

  Scenario: A team owner without an administrator team is forbidden
    Given the real API auth boundary "admin" receives identity "team-owner"
    Then the auth response status is 403
    And the auth response body is "forbidden"

  Scenario: An anonymous administrator request is rejected
    Given the real API auth boundary "admin" receives identity "anonymous"
    Then the auth response status is 401
    And the auth response body is "not authorized"

  Scenario: An authenticated request receives an OK status
    Given the real API auth boundary "authentication" receives identity "valid"
    Then the auth response status is 200

  Scenario: An authenticated request reaches the production user endpoint
    Given the real API auth boundary "authentication" receives identity "valid"
    Then the auth response identifies subject "brine-subject"
    And the auth response content type is "application/json"

  Scenario: An anonymous request to an authentication-required route receives unauthorized
    Given the real API auth boundary "authentication" receives identity "anonymous"
    Then the auth response status is 401

  Scenario: An anonymous request to an authentication-required route is rejected
    Given the real API auth boundary "authentication" receives identity "anonymous"
    Then the auth response body is "not authorized"

  Scenario: An expired supplied token receives unauthorized
    Given the real API auth boundary "authentication-if-provided" receives identity "expired"
    Then the auth response status is 401

  Scenario: An expired supplied token is rejected
    Given the real API auth boundary "authentication-if-provided" receives identity "expired"
    Then the auth response body is "not authorized"

  Scenario: A valid optional token receives an OK status
    Given the real API auth boundary "authentication-if-provided" receives identity "valid"
    Then the auth response status is 200

  Scenario: A valid optional token reaches the production signing-keys endpoint
    Given the real API auth boundary "authentication-if-provided" receives identity "valid"
    Then the auth response is the exact empty signing-keys document
    And the auth response content type is "application/json"

  Scenario: An omitted optional token receives an OK status
    Given the real API auth boundary "authentication-if-provided" receives identity "anonymous"
    Then the auth response status is 200

  Scenario: An omitted optional token reaches the production signing-keys endpoint
    Given the real API auth boundary "authentication-if-provided" receives identity "anonymous"
    Then the auth response is the exact empty signing-keys document
    And the auth response content type is "application/json"

  Scenario: A member of the requested team receives an OK status
    Given the real API auth boundary "team-authorization" receives identity "same-team"
    Then the auth response status is 200

  Scenario: A member of the requested team reaches the production pipelines endpoint
    Given the real API auth boundary "team-authorization" receives identity "same-team"
    Then the auth response lists pipeline "auth-pipeline"
    And the auth response content type is "application/json"

  Scenario: A member of another team is forbidden
    Given the real API auth boundary "team-authorization" receives identity "other-team"
    Then the auth response status is 403
    And the auth response body is "forbidden"

  Scenario: An anonymous team request is rejected
    Given the real API auth boundary "team-authorization" receives identity "anonymous"
    Then the auth response status is 401
    And the auth response body is "not authorized"
