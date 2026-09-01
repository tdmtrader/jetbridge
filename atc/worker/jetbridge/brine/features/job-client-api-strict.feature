Feature: The production jobs API exposes persisted job behavior

  Scenario: Administrator job listing returns jobs across persisted teams
    Given the production job boundary executes profile "api-list-admin"
    Then the production job observation exactly matches profile "api-list-admin"

  Scenario: Missing production job lookup returns HTTP 404
    Given the production job boundary executes profile "api-get-missing"
    Then the production job observation exactly matches profile "api-get-missing"

  Scenario: Missing production job build listing returns HTTP 404
    Given the production job boundary executes profile "api-builds-missing"
    Then the production job observation exactly matches profile "api-builds-missing"

  Scenario: Production pause endpoint persists authenticated pause state
    Given the production job boundary executes profile "api-pause-existing"
    Then the production job observation exactly matches profile "api-pause-existing"

  Scenario: Production pause endpoint returns HTTP 404 for a missing job
    Given the production job boundary executes profile "api-pause-missing"
    Then the production job observation exactly matches profile "api-pause-missing"

  Scenario: Production unpause endpoint clears persisted pause state
    Given the production job boundary executes profile "api-unpause-existing"
    Then the production job observation exactly matches profile "api-unpause-existing"

  Scenario: Production unpause endpoint returns HTTP 404 for a missing job
    Given the production job boundary executes profile "api-unpause-missing"
    Then the production job observation exactly matches profile "api-unpause-missing"

  Scenario: Production schedule endpoint advances persisted request time
    Given the production job boundary executes profile "api-schedule-existing"
    Then the production job observation exactly matches profile "api-schedule-existing"

  Scenario: Production schedule endpoint returns HTTP 404 for a missing job
    Given the production job boundary executes profile "api-schedule-missing"
    Then the production job observation exactly matches profile "api-schedule-missing"
