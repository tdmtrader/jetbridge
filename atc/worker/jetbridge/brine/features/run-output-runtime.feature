Feature: The Run runtime prepares and recovers an exact source

  @core-review
  Scenario: Preparation reserves a real source before giving the runtime control
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    Then its runtime control names the retained daemon source
    And its runtime grant establishes a signed source hold

  @core-review
  Scenario: Reconnecting retains the execution and refreshes its grants
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    And a new Run runtime prepares the producer again
    Then the repeated runtime control retains identity with fresh grants

  @core-review
  Scenario: Concurrent admission keeps one execution and source
    Given a Run producer and a ready output node
    When two Run runtimes prepare the producer concurrently
    Then the repeated runtime control retains identity with fresh grants

  @core-review
  Scenario: Unready nodes cannot reserve a source
    Given a Run producer and a ready output node
    When its output node becomes unready
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a start token

  @core-review
  Scenario: An aborted producer receives no execution authority
    Given a Run producer and a ready output node
    When its runtime producer is aborted
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a start token

  @core-review
  Scenario: Undeclared output selection has no source side effect
    Given a Run producer and a ready output node
    When its runtime declares no selected output
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a start token

  @core-review
  Scenario: Output overlap is refused before dispatching a source
    Given a Run producer and a ready output node
    When its runtime input overlaps the selected output
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a start token

  @core-review
  Scenario: A new runtime recovers a lost reservation reply after abort
    Given a Run producer and a ready output node
    When its source is dispatched but the database reply is lost
    And its runtime producer is aborted
    And a new Run runtime reconciles pending sources
    Then the original source is recorded without starting the producer
    When its Run runtime tries to prepare the producer
    Then the cancelled producer receives no runtime control

  @core-review
  Scenario: Recovery also finishes a dispatch interrupted before the request
    Given a Run producer and a ready output node
    When its source dispatch stops before contacting the daemon
    And its runtime producer is aborted
    And a new Run runtime reconciles pending sources
    Then the original source is recorded without starting the producer

  @core-review
  Scenario: A replacement node cannot inherit the original source
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    And its output node is replaced
    And its Run runtime tries to prepare the producer
    Then the replacement node receives no runtime control
