Feature: Pipeline lifecycle behavior over real PostgreSQL

  Scenario Outline: Production pipeline lifecycle behavior — <profile>
    Given the production pipeline lifecycle behavior "<profile>" is exercised
    Then the pipeline lifecycle behavior exactly matches "<profile>"

    Examples:
      | profile                 |
      | parent-and-child-live   |
      | parent-destroyed        |
      | child-already-archived  |
      | parent-archived         |
      | parent-job-removed      |
      | parent-build-newer      |
      | later-build-failed      |
      | no-parent               |
      | remove-event-tables     |
      | clear-deleted-pipelines |
      | missing-event-table     |
