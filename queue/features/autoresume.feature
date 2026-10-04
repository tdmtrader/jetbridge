Feature: A queue paused for no verdict resumes itself after a cool-down
  A run that gives no verdict is usually an outage that ends on its own, so
  the pause it causes ends by itself after the configured cool-down. Every
  other pause waits for an operator. Both ends of a pause are recorded.

  Scenario: A queue paused for no verdict resumes after the cool-down
    Given the cool-down is five minutes and a run ended with no verdict
    When the runner takes a step before the cool-down is over
    Then the queue is still paused
    When the runner takes a step after the cool-down is over
    Then the queue resumes and the resume is recorded as an auto-resume after the cool-down
    And a second no-verdict pause waits for its own cool-down

  Scenario: A queue paused for another reason waits for resume
    Given the cool-down is over on a queue paused because landing kept failing
    When the runner takes a step
    Then the queue is still paused and nothing is announced

  Scenario: A resume request for a pause the cool-down already ended does not clear the next
    Given a queue that auto-resumed and paused again
    When the operator asks for a resume of the first pause
    And the runner takes its next step
    Then the queue is still paused and the request is deleted

  Scenario: A no-verdict pause saved before auto-resume existed still resumes after the cool-down
    Given a queue paused for no verdict by an earlier version
    When the runner takes a step before the cool-down is over
    Then the queue is still paused
    When the runner takes a step after the cool-down is over
    Then the queue resumes

  Scenario: A queue paused for no verdict resumes after a restart
    Given a queue paused for no verdict and a new process that takes it over
    When the new process takes a step after the cool-down is over
    Then the queue resumes

  Scenario: A dead runner is auto-resumed once, then the pause holds
    Given a runner that never gives a verdict and a cool-down of five minutes
    When the runner takes steps through a pause, its auto-resume and a second pause
    Then the queue stays paused with a reason saying nothing auto-resumes it
    And it is never resumed again, and nothing is ejected

  Scenario: A new main allows one more auto-resume
    Given a held pause
    When main moves to a new commit and the runner takes a step
    Then the queue auto-resumes once more

  Scenario: A restart keeps the auto-resume count for the main
    Given a queue that auto-resumed once and paused again
    When a new process takes it over after the cool-down
    Then the pause is held, not resumed

  Scenario: A manual resume clears the hold
    Given a held pause
    When the operator resumes it and the queue pauses again
    Then it auto-resumes once more after the cool-down
