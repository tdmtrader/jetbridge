Feature: Landing a batch
  A batch is marked landed only once main has moved to its candidate. If
  moving main fails, nothing is settled and its changes wait to land.

  Scenario: A batch is marked landed only after main moves to it
    Given changes "a" and "b" are admitted
    When their batch passes and main moves to its candidate
    Then "a" and "b" were still waiting while main moved
    And "a" and "b" are landed

  Scenario: A batch whose landing fails stays queued
    Given changes "a" and "b" are admitted
    When their batch passes but main cannot be moved
    Then the landing error is reported
    And "a" and "b" are still waiting to land
