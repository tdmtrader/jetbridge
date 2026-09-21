Feature: A successful Run capture retains its own hidden candidate

  @core-review
  Scenario: Capture settlement protects the exact result generation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    Then one Run candidate claim protects that exact generation

  @core-review
  Scenario: A retained candidate allows its successful build to finish
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    Then its build is complete while its Run result remains unpublished

  @core-review
  Scenario: Cancellation after publication discards the candidate and releases the source
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its published producer is aborted
    And its Run records the published source release
    And its published producer finishes as "aborted"
    Then its build is complete with no candidate claim

  @core-review
  Scenario: An abort after candidate retention cannot be reported as success
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer is aborted
    And its published producer finishes as "succeeded"
    Then the published producer outcome is refused

  @core-review
  Scenario: A lost release commit answer preserves candidate identity
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And another Run controller repeats the published source release
    Then one Run candidate claim protects that exact generation

  @core-review
  Scenario: Candidate and source settlement roll back together
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run rolls back the published source release
    Then neither a candidate claim nor settled capture is visible
    When another Run controller repeats the published source release
    Then one Run candidate claim protects that exact generation
