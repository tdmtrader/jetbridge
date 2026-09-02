Feature: Volume core behavior over real PostgreSQL

  Scenario Outline: Production volume behavior — <profile>
    Given the production volume behavior "<profile>" is exercised
    Then the volume behavior exactly matches "<profile>"

    Examples:
      | profile                    |
      | failed-wrong-state         |
      | failed-missing             |
      | failed-return              |
      | failed-idempotent          |
      | created-wrong-state        |
      | created-missing            |
      | created-persisted          |
      | created-idempotent         |
      | artifact-fields            |
      | artifact-association       |
      | task-cache-replaces        |
      | container-fields           |
      | child-fields               |
      | parent-protected           |
      | base-resource-type-fields  |
      | task-identifier            |
      | child-lifecycle            |
