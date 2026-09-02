Feature: Job build selection over real PostgreSQL
  Production job queries distinguish finished from running work and preserve
  chronological build ordering.

  Scenario Outline: Job build selection behavior — <profile>
    Given the production job build selection behavior "<profile>" is exercised
    Then the job build selection behavior exactly matches "<profile>"

    Examples:
      | profile           |
      | finished-and-next |
      | chronological     |
