Feature: Aborting a Run build closes that build's own work

  Aborting one build of a v2 Run is scoped to that build. An aborted build
  that cannot finish over its open execution or unsettled output handoff
  records a build closure, and the cancellation worker closes that work
  through the node protocol. The Run keeps running and nothing asks for its
  cancellation.

  @core-review
  Scenario: An open execution closes on the node's acknowledgement and the job reruns
    Given a Run producer and a ready output node
    When its build is aborted over "an active execution"
    Then the build finishes aborted on the node's acknowledgement and its job admits a rerun

  @core-review
  Scenario: An unsettled handoff is released and the Run completes aborted
    Given a Run producer and a ready output node
    When its build is aborted over "an unsettled handoff"
    Then the Run completes aborted and its payload is reclaimed

  @core-review
  Scenario: An executing producer is stopped before its hold is released
    Given a Run producer and a ready output node
    When its build is aborted over "an executing producer"
    Then the producer is interrupted before its hold is released on exact evidence

  @core-review
  Scenario: A rerun that succeeds before the closure supersedes the aborted build
    Given a Run producer and a ready output node
    When its build is aborted over "a superseded handoff"
    Then the successful rerun supersedes it and the Run succeeds

  @core-review
  Scenario: Another build's live execution is untouched
    Given a Run producer and a ready output node
    When its build is aborted over "a sibling's live execution"
    Then the other build's live execution is untouched

  @core-review
  Scenario: Node loss keeps the build open until a restarted worker resumes
    Given a Run producer and a ready output node
    When its build is aborted over "a lost node"
    Then node loss keeps the build open until a restarted worker closes it
