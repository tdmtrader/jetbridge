Feature: Job task-cache clearing over real PostgreSQL
  Clearing by path or by step name must report the deleted row and remove the
  persisted cache for the selected production job.

  Scenario Outline: Job task-cache behavior — <profile>
    Given the production job task-cache behavior "<profile>" is exercised
    Then the job task-cache behavior exactly matches "<profile>"

    Examples:
      | profile              |
      | path-row-count       |
      | path-removes-cache   |
      | step-row-count       |
      | step-removes-cache   |
