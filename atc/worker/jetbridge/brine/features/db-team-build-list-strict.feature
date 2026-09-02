Feature: Team build lists over real PostgreSQL
  The production team queries must retain their visibility filters and inclusive
  boundaries. Each row corresponds to one exact remaining source leaf.

  Scenario Outline: Team build list behavior — <profile>
    Given the production team build list behavior "<profile>" is exercised
    Then the team build list behavior exactly matches "<profile>"

    Examples:
      | profile                         |
      | private-public-public-other     |
      | time-limit                      |
      | time-to-inclusive               |
      | time-from-inclusive             |
      | time-range-inclusive            |
      | builds-current-team             |
      | builds-from-inclusive           |
      | builds-to-inclusive             |
      | builds-range-inclusive          |
      | builds-private-other-team       |
      | builds-public-other-team        |
