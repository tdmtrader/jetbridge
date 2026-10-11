Feature: Trying the likeliest change first
  When a batch fails, the failed tests and the files each change touched can
  point at one change as the likely cause. Each change is ranked by how well
  the failed test names point at the directories it touched. The top suspect is
  tested alone on main and ejected only if it fails alone; if it passes, it
  lands. With no failed test names the batch is split in halves, and the log
  says so.

  Scenario: Suspects are ranked from the failed test names and the files each change touched
    Given changes "a", "b" and "c" touched different directories
    When the failed tests name the directories "c" and "b" touched, "c" more often
    Then "c" is the top suspect, then "b"
    And "a" is not a suspect

  Scenario: A correct hint ejects after one solo run without bisecting
    Given changes "a", "b", "c" and "d" are admitted and only "c" is broken
    And the failed tests point at "c"
    When their batch fails
    Then "c" is tested alone on main first, fails and is ejected
    And "a", "b" and "d" are tested together, pass and are landed

  Scenario: A wrong hint lands the innocent suspect, then bisect finds the culprit among the rest
    Given changes "a", "b", "c" and "d" are admitted and only "c" is broken
    And the failed tests point at "a"
    When their batch fails
    Then "a" is tested alone on main first, passes and is landed
    And "b", "c" and "d" are split in halves on the new main
    And "c" is ejected and "b" and "d" are landed

  Scenario: A stacked suspect runs with the changes it builds on, never alone
    Given changes "a", "b", "c" and "d" are admitted, "c" builds on "b" and only "d" is broken
    And the failed tests point at "c"
    When their batch fails
    Then "b" and "c" are tested together first, pass and are landed

  Scenario: With no failed test names the batch is split in halves and the log says so
    Given changes "a", "b", "c" and "d" are admitted
    And the test run names no failed tests
    When their batch fails
    Then the batch is split in halves as usual
    And the log says "no failed-test names; bisecting by halves"

  Scenario: Under proven-first a green suspect lands out of arrival order
    Given changes "a", "b", "c" and "d" are admitted and only "c" is broken
    And batch.order is proven-first, the default
    And the failed tests point at "b"
    When their batch fails
    Then "b" is tested alone on main first, passes and lands ahead of "a"

  Scenario: Under strict order a suspect runs alone only if it is first
    Given changes "a", "b", "c" and "d" are admitted and only "c" is broken
    And batch.order is strict
    And the failed tests point at "b"
    When their batch fails
    Then the batch is split in halves in arrival order and the log says why
    And "c" is ejected and "a", "b" and "d" are landed
