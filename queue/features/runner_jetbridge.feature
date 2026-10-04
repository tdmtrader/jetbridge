Feature: A JetBridge job tests each candidate
  The runner pins the candidate commit, runs the job, and reports a verdict only
  for a build that tested exactly that commit. Anything else is no verdict, so
  trouble with the build server never ejects a change.

  Scenario: A passing build of the candidate is green
    Given a JetBridge job that passes the candidate commit
    When the candidate is tested
    Then the verdict is pass
    And the candidate version was pinned and is unpinned afterwards

  Scenario: A failing build is red
    Given a JetBridge job that fails the candidate commit
    When the candidate is tested
    Then the verdict is fail

  Scenario: An errored or aborted build gives no verdict
    Given a JetBridge build that ends errored, or aborted
    When the candidate is tested
    Then there is no verdict

  Scenario: A build that tested a different commit gives no verdict
    Given a JetBridge build that succeeds but reports testing another commit
    When the candidate is tested
    Then there is no verdict

  Scenario: A build exceeding the wait cap gives no verdict and is aborted
    Given a JetBridge build that is still running past the wait cap
    When the candidate is polled
    Then there is no verdict
    And the build is asked to abort

  Scenario: An unreachable server gives no verdict, never red
    Given a JetBridge server that cannot be reached
    When the candidate is tested
    Then there is no verdict and never a fail

  Scenario: A build that tested the candidate under a different input name still binds by version; a different version never gives a verdict
    Given a JetBridge job that gets the candidate's resource under another input name
    When the candidate is tested
    Then the verdict is the build's
    Given a JetBridge build whose input named like the resource holds the candidate, while the resource gave it another version
    When the candidate is tested
    Then there is no verdict

  Scenario: A new Start supersedes the in-flight run: its build is aborted and unpinned, and its verdict can never be read
    Given a JetBridge run in flight with its build triggered
    When another candidate is started
    Then the earlier build is asked to abort and the version is unpinned
    And the earlier run gives no verdict

  Scenario: A 503 creating the job after the pin does not wedge the next Start
    Given a JetBridge server that refuses to create the job once the candidate is pinned
    When the next candidate is started
    Then the next candidate is pinned and tested

  Scenario: A 503 reading input_to after the build finished does not wedge the next Start
    Given a JetBridge server that cannot say which version a finished build tested
    When the next candidate is started
    Then the next candidate is pinned and tested
