Feature: Run completion and capture selection commit together

  @core-review
  Scenario: A successful producer has one durable Run checkpoint and capture reservation
    Given a Run producer with an authoritative "success" finish
    When its Run controller commits capture selection
    And a new Run controller repeats capture selection
    Then one Run checkpoint names the exact retained capture reservation

  @core-review
  Scenario: An uncommitted finish cannot authorize capture
    Given a Run producer with an authoritative "success" finish
    When its Run capture selection transaction rolls back
    Then neither a Run checkpoint nor a capture reservation exists

  @core-review
  Scenario Outline: Stage 2 refuses evidence that differs from the admitted producer
    Given a Run producer with an authoritative "success" finish
    When its Run controller offers a different "<fact>" for capture selection
    Then Run capture selection is refused without a checkpoint or reservation
    Examples:
      | fact             |
      | signature        |
      | pod              |
      | epoch            |
      | source hold     |
      | selected output  |
      | deadline         |
      | aborted build    |

  @core-review
  Scenario: A finish replay cannot replace its immutable checkpoint
    Given a Run producer with an authoritative "success" finish
    When its Run controller commits capture selection
    And its Run controller offers a different "checkpoint" for capture selection
    Then the first Run checkpoint and capture reservation remain unchanged

  @core-review
  Scenario: A source hold without a finish does not admit a Run checkpoint
    Given a Run producer with an authoritative "start only" finish
    When its Run controller commits capture selection
    Then Run capture selection is refused without a checkpoint or reservation

  @core-review
  Scenario: A failed producer records no-capture without a success checkpoint
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    Then the Run retains that exact no-capture decision and no success checkpoint

  @core-review
  Scenario: A capture decision closes defensive start admission
    Given a Run producer with an authoritative "success" finish
    When its Run controller commits capture selection
    And a new controller asks to admit that producer again
    Then producer start admission is closed by its finish decision

  @core-review
  Scenario: Cancellation before Stage 2 retains the cancellation branch
    Given a Run producer with an authoritative "success" finish
    When its Run controller records producer cancellation
    Then Run and Hangar retain pre-reservation cancellation without a success checkpoint

  @core-review
  Scenario: Cancellation after Stage 2 preserves the original capture checkpoint
    Given a Run producer with an authoritative "success" finish
    When its Run controller commits capture selection
    And its Run controller records producer cancellation
    Then one Run checkpoint names the exact retained capture reservation

  @core-review
  Scenario: The recovery reader observes a committed producer abort
    Given a Run producer with an authoritative "success" finish
    When its producer build is aborted
    Then Run capture recovery observes the cancellation request

  @core-review
  Scenario: Generic capture cannot bypass the owning Run transaction
    Given a Run producer with an authoritative "success" finish
    When a generic capture caller bypasses the Run checkpoint
    Then Run capture selection is refused without a checkpoint or reservation

  @core-review
  Scenario: Concurrent completion controllers retain one checkpoint
    Given a Run producer with an authoritative "success" finish
    When two Run controllers concurrently commit the finish
    Then one Run checkpoint names the exact retained capture reservation

  @core-review
  Scenario: Capture recovery maps the daemon finish through the Run checkpoint
    Given a Run producer with an authoritative "success" finish
    When capture recovery advances the Run producer
    Then one Run checkpoint names the exact retained capture reservation

  @core-review
  Scenario: An open Run handoff prevents fabricated terminal publication
    Given a Run producer with an authoritative "success" finish
    Then a direct terminal update cannot bypass its open handoff
