Feature: The Run runtime prepares and recovers an exact source

  @core-review
  Scenario: Preparation admits a pending capture before giving the runtime control
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    Then its runtime control names the retained capture
    And its runtime grant holds the capture's step directory

  @core-review
  Scenario: Reconnecting retains the execution and refreshes its grants
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    And a new Run runtime prepares the producer again
    Then the repeated runtime control retains identity with fresh grants

  @core-review
  Scenario: Concurrent admission keeps one execution and capture
    Given a Run producer and a ready output node
    When two Run runtimes prepare the producer concurrently
    Then the repeated runtime control retains identity with fresh grants

  @core-review
  Scenario: Unready nodes cannot admit a capture
    Given a Run producer and a ready output node
    When its output node becomes unready
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a capture

  @core-review
  Scenario: An aborted producer receives no execution authority
    Given a Run producer and a ready output node
    When its runtime producer is aborted
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a capture

  @core-review
  Scenario: Undeclared output selection has no capture side effect
    Given a Run producer and a ready output node
    When its runtime declares no selected output
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a capture

  @core-review
  Scenario: Output overlap is refused before admitting a capture
    Given a Run producer and a ready output node
    When its runtime input overlaps the selected output
    And its Run runtime tries to prepare the producer
    Then runtime preparation is refused without a capture

  @core-review
  Scenario: A replacement node cannot inherit the original capture
    Given a Run producer and a ready output node
    When its Run runtime prepares the producer
    And its output node is replaced
    And its Run runtime tries to prepare the producer
    Then the replacement node receives no runtime control
