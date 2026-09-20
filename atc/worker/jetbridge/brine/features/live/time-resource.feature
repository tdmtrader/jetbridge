@live-kubernetes
Feature: Actual resource publication and retry

  Scenario: The version a put created is published, and is the one the get after it fetches
    Given a build using the actual time resource
    When the time resource runs the "put-get" workflow
    Then the step succeeded
    And the time-resource workflow preserves its results

  Scenario: A retried step stops at the first attempt that succeeds
    Given a build using the actual time resource
    When the time resource runs the "retry" workflow
    Then the step succeeded
    And the time-resource workflow preserves its results

  # No publication alone is insufficient: an already-cancelled real worker
  # refuses execution even if a faulty retry loop enters the next attempt.
  Scenario: An aborted build does not spend its remaining attempts
    Given a build using actual Git and time resources
    And the Git server stops responding
    When the build aborts during its first real attempt
    Then the step was refused, saying "context canceled"
    And the aborted retry never enters its remaining attempt

  # Each non-abort row proves the guarded step really ran. The disconnect
  # leaves the hook's independent execution route healthy, so an erroneous
  # hook can actually publish. No resource replies or errors are fabricated.
  Scenario Outline: An on_abort hook runs on an abort and on nothing else — <case>
    Given a build using actual Git and time resources
    When the on_abort hook guards a real step that "<fate>"
    Then the real hook runs only for cancellation

    Examples:
      | case                            | fate        |
      | the build was aborted           | aborts      |
      | the step errored some other way  | disconnects |
      | the step failed without erroring | fails       |
      | the step did what it was asked   | succeeds    |
