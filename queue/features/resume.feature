Feature: An operator resumes a paused queue without stopping the runner
  The runner holds the queue's lease, so an operator cannot resume it
  directly. The operator asks by pushing a control ref; the runner acts on it
  at the start of its next step and deletes it once the queue is saved. A
  request names the pause it ends, so it can never clear a different one.

  Scenario: An operator resumes a paused queue while the runner is live
    Given the queue is paused and a runner holds its lease
    When the operator asks for a resume
    And the runner takes its next step
    Then the queue is no longer paused and the resume is announced
    And the request is deleted only after the queue is saved

  Scenario: A resume request on a queue that is not paused is deleted and changes nothing
    Given a runner holds the lease of a queue that is not paused
    When the operator asks for a resume
    And the runner takes its next step
    Then the request is deleted
    And nothing is announced and the saved state is unchanged

  Scenario: A resume request for an earlier pause does not clear a newer one
    Given a runner that is not paused and a run about to end with no verdict
    When the operator asks for a resume just before the queue pauses
    And the runner takes its next step
    Then the queue is still paused and nothing is announced
    And the request is deleted

  Scenario: A resume request whose delete failed is never applied to a later pause
    Given a paused queue, a request the runner cannot delete and a run that pauses again at once
    When the runner takes two steps
    Then the queue is resumed once, is paused again, and the request is applied only to the first pause

  Scenario: A control prefix that overlaps a ref the queue owns is refused
    Given a queue config file whose control prefix overlaps the admission prefix, the state ref or the lease ref
    When the config file is loaded
    Then loading is refused

  Scenario: With operators configured, a resume signed by an operator is honoured and any other is refused
    Given the queue lists its operators
    When a resume signed by an operator, one signed by someone else and an unsigned one are requested
    Then only the operator's request is honoured
    And the others are refused with the reason, which never names a key

  Scenario: With no operators configured, a resume request is honoured unsigned
    Given the queue lists no operators
    When an operator asks for a resume
    Then the request is honoured

  Scenario: A refused resume request is recorded and deleted and does not resume the queue
    Given a paused queue and a resume request that was refused
    When the runner takes its next step
    Then the queue is still paused and the refusal is announced and kept by status
    And the request is deleted
