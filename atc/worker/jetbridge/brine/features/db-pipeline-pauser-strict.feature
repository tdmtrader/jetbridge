Feature: Automatic pipeline pausing over real PostgreSQL

  Scenario Outline: Production pipeline pauser behavior — <profile>
    Given the production pipeline pauser profile "<profile>" is exercised
    Then the pipeline pauser observation exactly matches "<profile>"

    Examples:
      | profile        |
      | old-zero-job   |
      | old-all-jobs   |
      | pause-reason   |
      | recent-build   |
      | boundary-build |
      | newly-set      |
      | running-build  |
