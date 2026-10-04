Feature: Running the queue as a resource type
  The CI system drives the queue through two resources. Each check of the
  candidate resource takes one queue step and reports the run being tested, and
  a get hands its candidate to the test job; a put to the verdict resource
  records the test job's pass or fail. Every call is a fresh process that keeps
  nothing in memory; the queue's state lives in the repository.

  Scenario: A check of an empty queue finds no version
    Given no change is admitted
    When the resource is checked
    Then it reports no version

  Scenario: A check composes an admitted change and starts its test run
    Given a change is admitted
    When the resource is checked
    Then it reports one version naming the run and its candidate
    And the run is kept in the repository pointing at the candidate

  Scenario: The queue lands a change after its test job records a pass
    Given a change is admitted and the resource was checked
    When the test job gets the run, builds its candidate and records a pass
    And the candidate's own files are left as they are beside the run's details
    And the resource is checked again
    Then main holds the change, landed under its id with its original commit

  Scenario: A change whose test job records a fail is ejected
    Given a change is admitted and the resource was checked
    When the test job records a fail
    And the resource is checked again
    Then the change is ejected and main is unchanged

  Scenario: Two checks with no test result give the same version and start no second run
    Given a change is admitted
    When the resource is checked twice with no test result between
    Then both checks report the same version
    And only one run was started

  Scenario: A test job that errors never counts as a failure
    Given a change is admitted and the resource was checked
    When the test job records nothing until the wait cap passes
    And the resource is checked again
    Then the change is not ejected
    And the stats count one wait cap expiry

  Scenario: A put with a verdict other than pass or fail is refused
    Given a change is admitted and the resource was checked
    When the test job puts a verdict that is neither pass nor fail
    Then the put fails and records nothing

  Scenario: A source with no known mode is refused and never runs the queue
    Given a change is admitted
    When the resource is checked with no mode, or a mode it does not know
    Then the check fails naming the mode
    And the queue has not been touched

  Scenario: A pass with the hook's commit records both in one push
    Given a change is admitted and the resource was checked
    And the test job made one commit on the candidate
    When the test job records a pass with that commit
    Then the pass and the commit are both recorded for the run
    And neither can be recorded again

  Scenario: A hook commit that is not one commit on the candidate is refused, and nothing is recorded
    Given a change is admitted and the resource was checked
    When the test job records a pass with two commits on the candidate, or one commit on another parent
    Then the put fails
    And no verdict and no commit are recorded

  Scenario: A get of a candidate that holds its own .mq dir is refused
    Given an admitted change adds a .mq dir of its own and the resource was checked
    When the test job gets the run
    Then the get fails naming .mq
