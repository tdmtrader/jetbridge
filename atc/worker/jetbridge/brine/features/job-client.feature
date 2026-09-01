Feature: The Go Concourse job client crosses the production API boundary

  Scenario: Pipeline job listing returns the persisted instanced job
    Given the production job boundary executes profile "client-list-pipeline"
    Then the production job observation exactly matches profile "client-list-pipeline"

  Scenario: Global job listing returns the persisted instanced job
    Given the production job boundary executes profile "client-list-all"
    Then the production job observation exactly matches profile "client-list-all"

  Scenario: Existing job lookup returns its production representation
    Given the production job boundary executes profile "client-get-existing"
    Then the production job observation exactly matches profile "client-get-existing"

  Scenario: Missing job lookup returns not found without an error
    Given the production job boundary executes profile "client-get-missing"
    Then the production job observation exactly matches profile "client-get-missing"

  Scenario: Unbounded job build listing returns every persisted build
    Given the production job boundary executes profile "client-builds-all"
    Then the production job observation exactly matches profile "client-builds-all"

  Scenario: From cursor returns the exact persisted build suffix
    Given the production job boundary executes profile "client-builds-from"
    Then the production job observation exactly matches profile "client-builds-from"

  Scenario: From cursor with limit returns the exact oldest persisted build
    Given the production job boundary executes profile "client-builds-from-limit"
    Then the production job observation exactly matches profile "client-builds-from-limit"

  Scenario: To cursor returns the exact persisted build prefix
    Given the production job boundary executes profile "client-builds-to"
    Then the production job observation exactly matches profile "client-builds-to"

  Scenario: To cursor with limit returns the exact bounded persisted build
    Given the production job boundary executes profile "client-builds-to-limit"
    Then the production job observation exactly matches profile "client-builds-to-limit"

  Scenario: Combined cursors return only the exact persisted range
    Given the production job boundary executes profile "client-builds-from-to"
    Then the production job observation exactly matches profile "client-builds-from-to"

  Scenario: Missing job build listing returns not found without an error
    Given the production job boundary executes profile "client-builds-missing"
    Then the production job observation exactly matches profile "client-builds-missing"

  Scenario: Bounded job build listing decodes production pagination links
    Given the production job boundary executes profile "client-pagination-links"
    Then the production job observation exactly matches profile "client-pagination-links"

  Scenario: Unbounded job build listing returns empty pagination
    Given the production job boundary executes profile "client-pagination-empty"
    Then the production job observation exactly matches profile "client-pagination-empty"

  Scenario: Pausing an existing job persists the authenticated identity
    Given the production job boundary executes profile "client-pause-existing"
    Then the production job observation exactly matches profile "client-pause-existing"

  Scenario: Pausing a missing job returns not found without an error
    Given the production job boundary executes profile "client-pause-missing"
    Then the production job observation exactly matches profile "client-pause-missing"

  Scenario: Unpausing an existing job clears persisted pause state
    Given the production job boundary executes profile "client-unpause-existing"
    Then the production job observation exactly matches profile "client-unpause-existing"

  Scenario: Unpausing a missing job returns not found without an error
    Given the production job boundary executes profile "client-unpause-missing"
    Then the production job observation exactly matches profile "client-unpause-missing"

  Scenario: Scheduling an existing job advances its persisted request time
    Given the production job boundary executes profile "client-schedule-existing"
    Then the production job observation exactly matches profile "client-schedule-existing"

  Scenario: Scheduling a missing job returns not found without an error
    Given the production job boundary executes profile "client-schedule-missing"
    Then the production job observation exactly matches profile "client-schedule-missing"
