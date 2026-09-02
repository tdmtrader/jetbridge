Feature: Production step metadata environment generation

  Scenario Outline: Production step metadata behavior — <profile>
    Given the production step metadata profile "<profile>" is exercised
    Then the step metadata observation exactly matches "<profile>"

    Examples:
      | profile           |
      | full-env          |
      | instance-vars     |
      | sparse-env        |
      | full-task-env     |
      | empty-task-env    |
