Feature: Cancellation records handoff classification before stopping work

  @core-review
  Scenario Outline: An exact node observation becomes a durable cleanup prerequisite
    Given a Run producer with an authoritative "<state>" finish
    When cancellation retains its initial handoff classification
    Then the initial classification survives a replacement observer
    Examples:
      | state         |
      | start only    |
      | success       |
      | never started |
