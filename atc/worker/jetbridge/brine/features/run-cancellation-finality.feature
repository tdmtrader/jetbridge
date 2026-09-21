Feature: Cancellation publishes one quiescent aborted Run

  @core-review
  Scenario: Cancellation converges after a lost notification
    Given a Run with resource checks and a ready output node
    When the periodic cancellation component recovers a lost notification
    Then its aborted Run is immutable and has no public results

  @core-review
  Scenario Outline: Cancellation settles all owned builds before publication
    Given a Run with resource checks and a ready output node
    When cancellation workers settle its remaining "<case>"
    Then its aborted Run is immutable and has no public results

    Examples:
      | case |
      | unstarted builds |
      | owned check |
      | naturally completed build |
      | expired worker |

  @core-review
  Scenario: The complete worker releases a producer before completing its build
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its real worker binds the producer execution
    When cancellation workers settle its remaining "prepared source"
    Then its aborted Run is immutable and has no public results

  @core-review
  Scenario: A lost source reservation reply blocks build abort and publication
    Given a Run producer and a ready output node
    And its source is dispatched but the database reply is lost
    When cancellation workers settle its remaining "unresolved source"
    Then its unresolved source prevents an aborted publication

  @core-review
  Scenario: Aborted publication releases the hidden candidate claim atomically
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its runtime producer publishes a successful review
    And its Run records the published source release
    When cancellation workers settle its hidden review
    Then its aborted Run is immutable and has no public results

  @core-review
  Scenario: A refused terminal commit preserves the hidden claim for recovery
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its runtime producer publishes a successful review
    And its Run records the published source release
    When cancellation recovers from a refused publication commit
    Then its aborted Run is immutable and has no public results
