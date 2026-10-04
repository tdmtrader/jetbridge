Feature: Every settled change leaves a record
  Each land, eject and pause is kept, with the time it happened and why, so an
  operator can read what the queue decided long after the event was announced.

  Scenario: Every landed change leaves a timestamped settle record
    Given changes "a" and "b" are admitted
    When the queue runs and their batch passes
    Then the saved state holds a record for "a" and for "b" as landed
    And each record carries the time, the commit, the run and the batch

  Scenario: An ejected change records when it was ejected and why
    Given changes "a", "b" and "c" are admitted
    And "c" builds on "b"
    And only "b" is broken
    When the queue runs
    Then the saved state records "b" as ejected at that time because it failed on its own
    And it records "c" as ejected with the cause naming "b"

  Scenario: A flaky batch is recorded and shown, never hidden
    Given changes "a" and "b" are admitted
    When their batch fails but each half passes on its own
    Then the saved state holds one flaky record naming "a" and "b"
    And the record carries the run, the batch and why it is flaky
    And the status lists the flake

  Scenario: A flake is saved with the outcome it accompanies
    Given changes "a" and "b" are admitted and their batch is flaky
    When the store fails on the save after the landing
    Then the saved state holds the landing and its flake, or neither

  Scenario: An eject keeps the failing test names and the build they came from
    Given a change "b" that fails with the tests "TestOne" and "TestTwo" on a named build
    When the queue runs and ejects it
    Then its eject record keeps the failing test names and the build
    And the operator sees them when listing the ejected changes
