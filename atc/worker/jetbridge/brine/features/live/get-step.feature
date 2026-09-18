@live-kubernetes
Feature: Git gets preserve build-visible results

  Scenario: A get step fetches the version its plan pinned
    Given a build fetching from a real Git repository
    When the Git get selects the "first" commit
    Then the step succeeded
    And the build preserves the Git get outcome

  Scenario: A get the resource refuses is a failed build, and hands nothing downstream
    Given a build fetching from a real Git repository
    When the Git get selects the "missing" commit
    Then the step failed rather than erroring
    And the build preserves the Git get outcome

  # The resource must be executing a real HTTP fetch before its 15s deadline.
  Scenario: A get that outruns its timeout says so, and never reports a finish
    Given a build fetching from a real Git repository
    And the Git server stops responding
    When the Git get selects the "first" commit
    Then the step failed rather than erroring
    And the build log records the error "timeout exceeded"
    And the build never reported the get finishing
    And the build preserves the Git get outcome
