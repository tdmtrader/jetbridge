Feature: Resource config factory behavior over real PostgreSQL

  Scenario Outline: Production resource config factory behavior — <profile>
    Given the production resource config factory profile "<profile>" is exercised
    Then the resource config factory observation exactly matches "<profile>"

    Examples:
      | profile             |
      | recent-reference    |
      | idempotent-create   |
      | cleanup-zero        |
      | cleanup-grace       |
      | find-base-config    |
      | find-custom-config  |
      | find-missing-config |
      | concurrent-delete-create |
