Feature: Trying the likeliest change first
  When a batch fails, the failed tests and the files each change touched can
  point at one change as the likely cause. That hint is only a guess, so the
  change is tried on its own first. It is ejected only if it fails on its
  own. If it passes, the hint was wrong and the batch is split in halves as
  usual.

  Scenario: A correct hint ejects after one solo run without bisecting
    Given changes "a", "b", "c" and "d" are admitted
    And only "c" is broken
    And the failed tests point at "c"
    When their batch fails
    Then "c" is tried on its own first, fails and is ejected
    And "a", "b" and "d" are tried together, pass and are landed
    And the hint is counted as a hit

  Scenario: A wrong hint falls back to bisect and the innocent change lands
    Given changes "a", "b", "c" and "d" are admitted
    And only "c" is broken
    And the failed tests point at "a"
    When their batch fails
    Then "a" is tried on its own first and passes
    And the batch is split in halves as usual
    And "c" is ejected and "a", "b" and "d" are landed
    And the hint is counted as a miss
