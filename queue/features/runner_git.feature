Feature: Testing candidates through results kept in git
  Each run is kept in the repository, pointing at the candidate it tests, and
  each test result is written once beside it, naming the candidate it was for.
  Nothing is kept in memory, so a restarted queue picks up where it left off.
  A result counts only for the candidate the run tests now, and a run with no
  result in time gives no verdict, never a failure.

  Scenario: Starting the same run on the same candidate twice changes nothing
    Given a run was started on a candidate
    When it is started again on the same candidate
    Then the recorded run is unchanged

  Scenario: A test result recorded for an older candidate is not used
    Given a run was started on one candidate and then on a newer one
    When a passing result is recorded for the older candidate
    Then the run is still waiting for a result

  Scenario: A passing or failing test result is reported as pass or fail
    Given a run on a candidate
    When a failing result is recorded for that candidate
    Then the run reports fail
    And a run with a passing result reports pass

  Scenario: A run with no test result waits, then gives no verdict after the wait cap
    Given a run on a candidate with no result
    When it is checked before the wait cap
    Then it is still waiting
    When it is checked after the wait cap
    Then it is done with no verdict

  Scenario: A restarted runner sees the same runs and results
    Given a run with a passing result
    When a newly started runner checks the run
    Then it reports pass

  Scenario: A recorded test result cannot be overwritten
    Given a run with a passing result
    When a failing result is recorded for the same candidate
    Then the second result is refused
    And the run still reports pass
