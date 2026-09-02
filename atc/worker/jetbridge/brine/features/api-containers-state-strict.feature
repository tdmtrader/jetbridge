Feature: Container query API behavior over real HTTP and PostgreSQL

  Scenario Outline: Production container query API behavior — <profile>
    Given the production container query API behavior "<profile>" is exercised
    Then the container query API behavior exactly matches "<profile>"

    Examples:
      | profile              |
      | list-status          |
      | list-content-type    |
      | list-body            |
      | list-empty-status    |
      | list-empty-body      |
      | get-missing          |
      | get-status           |
      | get-content-type     |
      | get-body             |
      | get-outside-team     |
