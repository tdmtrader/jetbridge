Feature: A successful Run capture holds its own claim until the Run binds it

  @core-review
  Scenario: A published capture protects the exact result generation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    Then one Run capture claim protects that exact generation

  @core-review
  Scenario: A published capture allows its successful build to finish
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its published producer finishes as "succeeded"
    Then its build is complete while its Run result remains unpublished

  @core-review
  Scenario: An aborted build may finish over its published capture
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its published producer is aborted
    And its published producer finishes as "aborted"
    Then its build is complete while its Run result remains unpublished

  @core-review
  Scenario: An abort after publication cannot be reported as success
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its published producer is aborted
    And its published producer finishes as "succeeded"
    Then the published producer outcome is refused
