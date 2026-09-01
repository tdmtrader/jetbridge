Feature: Wall API behavior through the production HTTP and PostgreSQL boundary

  Source: all 14 leaf specs in atc/api/wall_test.go. Every scenario uses a
  real TCP listener, http.Server, production route, accessor/authentication
  handlers, wall server, and PostgreSQL wall object.

  Scenario: Getting a persisted wall message succeeds
    When the production wall API handles profile "get-status"
    Then the wall API returned status 200

  Scenario: Getting a persisted wall message returns the JSON content type
    When the production wall API handles profile "get-content-type"
    Then the wall API content type is "application/json"

  Scenario: Getting a permanent wall message returns only its exact message
    When the production wall API handles profile "get-permanent-document"
    Then the returned wall document contains the permanent message only

  Scenario: Getting an expiring wall message returns its message and remaining TTL
    When the production wall API handles profile "get-expiring-document"
    Then the returned wall document contains the expiring message and a bounded TTL

  Scenario: An administrator can set a wall message
    When the production wall API handles profile "set-status"
    Then the wall API returned status 200

  Scenario: Setting a wall message persists its message and expiration
    When the production wall API handles profile "set-state"
    Then the persisted wall has a bounded one minute TTL

  Scenario: Setting an empty wall message returns the exact bad request
    When the production wall API handles profile "set-invalid-response"
    Then the wall API returned status 400
    And the wall API returned the exact body "Wall message cannot be empty"

  Scenario: Setting an empty wall message stores no wall
    When the production wall API handles profile "set-invalid-state"
    Then the persisted wall message is ""

  Scenario: A non-administrator cannot set a wall message
    When the production wall API handles profile "set-forbidden"
    Then the wall API returned status 403

  Scenario: An unauthenticated request cannot set a wall message
    When the production wall API handles profile "set-unauthorized"
    Then the wall API returned status 401

  Scenario: An administrator can clear a wall message
    When the production wall API handles profile "clear-status"
    Then the wall API returned status 200

  Scenario: Clearing a wall message removes the persisted banner
    When the production wall API handles profile "clear-state"
    Then the persisted wall message is ""

  Scenario: A non-administrator cannot clear a wall message
    When the production wall API handles profile "clear-forbidden"
    Then the wall API returned status 403

  Scenario: An unauthenticated request cannot clear a wall message
    When the production wall API handles profile "clear-unauthorized"
    Then the wall API returned status 401
