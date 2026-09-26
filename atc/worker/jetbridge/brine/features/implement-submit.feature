@implement @review-linux
Feature: A local receipt preserves a detached implement invocation

  The implement workload rides the same detached client as review: a sealed
  snapshot is uploaded as the Run's snapshot input, the author task's change
  result receives the credential handoff, and the receipt records which
  workload it belongs to. Completed submissions read the uploaded snapshot
  through Hangar's real node materializer, run the real worker in implement
  mode, and are retrieved and applied from fresh processes. Only the model
  process is substituted.

  Background:
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "an implement input and result"

  Scenario Outline: Interrupting submission keeps the same durable Run
    Then a saved implement submission encounters <case>

    Examples:
      | case |
      | "interruption before readiness" |
      | "a fresh client replay" |
      | "a fresh CLI replay" |
      | "a changed input" |
      | "a receipt replayed by another workload" |
      | "the installed implement template" |
      | "ready CLI" |
