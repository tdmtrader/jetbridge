Feature: Job reruns over real PostgreSQL
  Production reruns retain their original build identity, increment their rerun
  number, remain queryable, and request scheduling for the correct job.

  Scenario Outline: Job rerun behavior — <profile>
    Given the production job rerun behavior "<profile>" is exercised
    Then the job rerun behavior exactly matches "<profile>"

    Examples:
      | profile             |
      | persisted           |
      | requests-schedule   |
      | increments          |
      | rerun-of-rerun      |
