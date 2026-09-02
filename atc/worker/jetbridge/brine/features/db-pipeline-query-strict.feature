Feature: Pipeline query behavior over real PostgreSQL

  Scenario Outline: Production pipeline query behavior — <profile>
    Given the production pipeline query behavior "<profile>" is exercised
    Then the pipeline query behavior exactly matches "<profile>"

    Examples:
      | profile          |
      | version-enabled  |
      | version-disabled |
      | time-none        |
      | time-limit       |
      | time-to          |
      | time-from        |
      | time-range       |
      | parent-fields    |
      | parent-invalid   |
      | parent-newer     |
      | parent-null      |
