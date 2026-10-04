Feature: Reading what composing a batch onto main gave back
  Composing either gives a candidate or names the one change that does not
  merge. Anything else, such as a record that cannot be read, a timeout, or a
  conflict naming a change that is not in the batch, says nothing about any
  change. It counts as no verdict: retried, then paused, never ejected.

  Scenario: An unreadable compose result pauses the queue and ejects nobody
    Given one retry is allowed when there is no verdict
    And changes "a" and "b" are admitted
    When composing their batch gives back a result that cannot be read
    Then the batch is retried
    When the retry gives back an unreadable result again
    Then the queue pauses
    And "a" and "b" are still waiting to land

  Scenario: A named conflict ejects only that change
    Given changes "a" and "b" are admitted
    And composing names "b" as the change that does not merge
    When their batch is composed
    Then the batch is split
    And "a" composes on its own and is landed
    And "b" is ejected
