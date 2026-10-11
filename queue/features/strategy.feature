Feature: A landing strategy decides what runs and how each result settles
  A strategy reads the queue and says which changes to test next, then turns
  each result into settlements: land, eject or pause. It never lands anything
  itself and never changes the queue. The serial strategy, the default, tests
  one batch at a time and makes the same decisions the queue always has.

  Scenario: A green batch is settled to land
    Given changes "a", "b" and "c" are admitted
    When their batch passes
    Then "a", "b" and "c" are settled to land

  Scenario: A red batch is bisected and the green half lands
    Given changes "a", "b", "c" and "d" are admitted
    And only "d" is broken
    When their batch fails
    Then the batch is split in halves until "d" fails on its own
    And "a", "b" and "c" are settled to land
    And "d" is ejected

  Scenario: A missing verdict is retried then the queue pauses
    Given changes "a" and "b" are admitted
    And one retry is allowed
    When their batch gets no verdict twice
    Then the queue pauses with "a" and "b" still queued
    And no change is ejected

  Scenario: A change built on an ejected change never runs
    Given changes "a", "b" and "c" are admitted
    And "c" builds on "b"
    And only "b" is broken
    When their batch fails
    Then "b" is ejected
    And "c" is ejected without running alone, naming "b" as the cause

  Scenario: A change built on an ejected change outside the batch is ejected, not left waiting
    Given changes "a" and "b" are admitted
    And batches hold one change
    And "b" builds on "a"
    And only "a" is broken
    When "a" fails on its own
    Then "a" is ejected
    And "b" is ejected without running, naming "a" as the cause

  Scenario: A change admitted after the change it builds on was ejected is ejected without running
    Given "a" was ejected
    And change "b", which builds on "a", is admitted
    When the next batch is chosen
    Then "b" is ejected without running, naming "a" as the cause

  Scenario: A flaky batch is surfaced in the outcome
    Given changes "a" and "b" are admitted
    When their batch fails but both halves pass
    Then "a" and "b" are settled to land
    And the outcome names the batch of "a" and "b" as flaky

  Scenario: A change waiting on an unlanded change stays queued
    Given changes "a" and "b" are admitted
    And "b" builds on a change that is neither landed nor queued
    When the next batch is chosen
    Then only "a" runs
    And "b" stays queued

  Scenario: The strategy defaults to serial and an unknown one is refused
    Given a queue config file that does not name a strategy
    When the config file is loaded
    Then the strategy is serial
    And a config file naming an unknown strategy is refused, listing the allowed ones
