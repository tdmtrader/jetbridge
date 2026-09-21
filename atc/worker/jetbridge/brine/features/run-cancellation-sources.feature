Feature: Cancellation workers settle the exact retained output source

  @core-review
  Scenario: A worker-owned capture uses the retained source proof for execution closure
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its real worker binds the producer execution
    When cancellation workers settle its never-started source
    Then its producer execution is closed without a fabricated process outcome

  @core-review
  Scenario: A deferred database refusal remains typed across cancellation commits
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    When cancellation encounters a deferred policy refusal and retries after it clears
    Then the worker has retained closure and the daemon has released the source

  @core-review
  Scenario: A disconnected producer is closed without ever starting
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    When cancellation workers settle its never-started source
    Then the worker has retained closure and the daemon has released the source

  @core-review
  Scenario: A node name cannot transfer the source to a replacement node
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its output node is replaced
    When cancellation encounters its "replacement node"
    Then cancellation preserves the unresolved source

  @core-review
  Scenario: An unanswered dispatch remains pending
    Given a Run producer and a ready output node
    And its source is dispatched but the database reply is lost
    When cancellation encounters its "unanswered dispatch"
    Then cancellation preserves the unresolved source

  @core-review
  Scenario: Dispatch recovery allows a later worker to release the original source
    Given a Run producer and a ready output node
    And its source is dispatched but the database reply is lost
    And cancellation encounters its "unanswered dispatch"
    And a new Run runtime reconciles pending sources
    When cancellation workers settle its never-started source
    Then the worker has retained closure and the daemon has released the source

  @core-review
  Scenario: The init hold is recovered before its source is released
    Given a Run producer and a ready output node
    And its Run runtime prepares the producer
    And its runtime grant establishes a signed source hold
    When cancellation workers settle its never-started source
    Then the worker has retained closure and the daemon has released the source
