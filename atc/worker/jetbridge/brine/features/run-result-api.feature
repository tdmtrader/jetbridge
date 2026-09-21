Feature: Authorized clients read the durable Run terminal observation

  @core-review
  Scenario Outline: Result visibility follows team access before and after reclamation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "<caller>" client reads the retained Run result
    And its disposable payload is reclaimed after a newer Run
    And a fresh "<caller>" client reads the retained Run result
    Then the header reader returns the retained result and version

    Examples:
      | caller     |
      | owner      |
      | viewer     |
      | anonymous  |
      | cross-team |

  @core-review
  Scenario: Pending candidate claims are not exposed in Run detail
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And a fresh "owner" client reads the retained Run result
    Then the aggregate Run remains running with no public result

  @core-review
  Scenario Outline: Non-success has a terminal version and an explicit empty map
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "<status>"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "owner" client reads the retained Run result
    Then the Run publishes "<status>" with an explicit empty result

    Examples:
      | status  |
      | failed  |
      | errored |
