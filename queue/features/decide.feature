Feature: Deciding what happens to a batch after its test run
  The run gives one verdict for the whole batch. A pass lands it. A run that
  gives no verdict at all says nothing about the changes, so it is retried and
  then paused, but no change is ever ejected for it.

  Scenario: A green batch lands
    Given changes "a" and "b" are admitted
    When their batch passes
    Then "a" and "b" are landed
    And the next batch is empty

  Scenario: No verdict retries and never ejects
    Given one retry is allowed when there is no verdict
    And change "a" is admitted
    When its batch ends with no verdict
    Then the batch is retried
    When the retry also ends with no verdict
    Then the queue pauses
    And "a" is still waiting to land
