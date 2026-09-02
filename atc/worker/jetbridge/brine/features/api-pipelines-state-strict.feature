Feature: Pipeline state API behavior over real HTTP and PostgreSQL

  Scenario Outline: Production pipeline state API behavior — <profile>
    Given the production pipeline state API behavior "<profile>" is exercised
    Then the pipeline state API behavior exactly matches "<profile>"

    Examples:
      | profile        |
      | delete         |
      | pause          |
      | archive        |
      | unpause        |
      | expose         |
      | hide           |
      | order-global   |
      | order-instance |
