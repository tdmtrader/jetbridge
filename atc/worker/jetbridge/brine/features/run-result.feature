Feature: A Run atomically publishes one complete immutable result

  @core-review
  Scenario: Successful completion publishes the exact protected candidate
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the exact candidate is the complete successful Run result

  @core-review
  Scenario: A failed sibling hides results and releases the candidate
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "failed"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the Run publishes "failed" with an explicit empty result

  @core-review
  Scenario: An errored sibling hides results and releases the candidate
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "errored"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the Run publishes "errored" with an explicit empty result

  @core-review
  Scenario: Pending sibling work prevents terminal publication
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the aggregate Run remains running with no public result

  @core-review
  Scenario: Unconsumed scheduling requests prevent terminal publication
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And the aggregate Run completion is attempted
    Then the aggregate Run remains running with no public result

  @core-review
  Scenario: Otherwise successful work with no candidate is an error
    Given an internally admitted v2 result Run
    When its uncaptured result producer finishes successfully
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the Run publishes "errored" with an explicit empty result

  @core-review
  Scenario: Rollback preserves the running Run and retained candidate
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "failed"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is rolled back
    Then the aggregate Run remains running with no public result
    When the aggregate Run completion is attempted
    Then the Run publishes "failed" with an explicit empty result

  @core-review
  Scenario: Completion replay retains its immutable version and publication
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the exact candidate is the complete successful Run result
    When the terminal Run observation is remembered
    And the aggregate Run completion is attempted
    Then replaying completion preserves the same terminal observation
    And direct changes to its published result are refused


  @core-review
  Scenario: Recovery finalizes without the original build tracker
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And a fresh capture component finalizes pending Runs
    Then the exact candidate is the complete successful Run result

  @core-review
  Scenario: Rerun candidate selection releases the superseded claim
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And the selected job reruns with "a new candidate"
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the exact candidate is the complete successful Run result
    And superseded result claims are released

  @core-review
  Scenario: An older candidate cannot substitute for the effective rerun
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And the selected job reruns with "no candidate"
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the Run publishes "errored" with an explicit empty result

  @core-review
  Scenario: Terminal publication closes late build writes
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then late build completion cannot change the selected outcomes

  @core-review
  Scenario: Generic release cannot bypass a running candidate lifetime
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    Then a generic release cannot discard its protected candidate claim

  @core-review
  Scenario: Generic release cannot bypass a published result lifetime
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then a generic release cannot discard its protected candidate claim

  @core-review
  Scenario Outline: Later job failure can finish a captured producer without publishing its result
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "<status>"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    Then the Run publishes "<status>" with an explicit empty result
    Examples:
      | status  |
      | failed  |
      | errored |

  @core-review
  Scenario: Result identity and claim survive payload reclamation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And the terminal Run observation is remembered
    And its disposable payload is reclaimed after a newer Run
    And the aggregate Run completion is attempted
    Then replaying completion preserves the same terminal observation
    And the header reader returns the retained result and version
