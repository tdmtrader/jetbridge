Feature: Resource cache durable keys over real PostgreSQL

  Scenario Outline: Production resource cache durable-key behavior — <profile>
    Given the production resource cache durable-key profile "<profile>" is exercised
    Then the resource cache durable-key observation exactly matches "<profile>"

    Examples:
      | profile                         |
      | survives-delete-and-recreate    |
      | distinguishes-version           |
      | distinguishes-source            |
      | distinguishes-params            |
      | distinguishes-custom-type-parent |
      | readable-after-load-by-id        |
      | backfills-pre-column-row         |
      | accepted-artifact-key-format     |
