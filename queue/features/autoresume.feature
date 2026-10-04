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
