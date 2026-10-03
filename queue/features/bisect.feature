Feature: Proving which change broke a red batch
  A red batch of several changes says only that something in it is broken.
  The batch is split into halves and the first half is tried first, again and
  again, until a change fails on its own. Only that change is ejected. A
  change is never ejected on a guess.

  Scenario: One bad change in four is ejected, three land
    Given changes "a", "b", "c" and "d" are admitted
    And only "c" is broken
    When their batch fails
    Then "a" then "b" is tried first and passes
    And "c" fails on its own and is ejected
    And "a", "b" and "d" are landed

  Scenario: Halves both pass so the batch lands and a flake is recorded
    Given changes "a", "b", "c" and "d" are admitted
    When their batch fails
    And "a" then "b" passes
    And "c" then "d" passes
    Then "a", "b", "c" and "d" are landed
    And the failed batch is recorded as a flake
    And no change is ejected
