Feature: A Run owns admission of its exact output producer

  @core-review
  Scenario: Concurrent controllers recover one admitted execution
    Given an internally admitted v2 result Run
    When two controllers concurrently admit the producer
    Then one non-authorizing handoff belongs to that exact Run and build

  @core-review
  Scenario: A pending output handoff prevents externally completing its build
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And its build tries to finish before output disposition
    Then the build remains unfinished and the handoff is retained

  @core-review
  Scenario: A start token cannot be removed to permit another execution
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And deletion of its start token is attempted
    Then the original start remains immutable

  @core-review
  Scenario: A legacy Run is never presented as a v2 invocation
    Given a Run admitted from a parameterized template
    Then the Run records an immutable legacy birth contract

  @core-review
  Scenario Outline: V2 admission requires the current activated contract
    Given internal v2 admission with "<activation>" activation
    Then v2 admission is refused before allocating a Run
    Examples:
      | activation |
      | disabled   |
      | stale      |

  @core-review
  Scenario: Starting a result producer predeclares exactly one handoff
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    Then one non-authorizing handoff belongs to that exact Run and build

  @core-review
  Scenario: Reconnecting repeats the original producer admission
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And a new controller repeats the producer admission
    Then one non-authorizing handoff belongs to that exact Run and build

  @core-review
  Scenario: Rolling back start admission leaves no orphan handoff
    Given an internally admitted v2 result Run
    When its producer admission transaction is rolled back
    Then neither a start token nor a Hangar handoff remains

  @core-review
  Scenario Outline: Unadmitted producer facts cannot select an output
    Given an internally admitted v2 result Run
    When its producer asks to start with a different "<fact>"
    Then start admission is refused without a handoff
    Examples:
      | fact          |
      | task          |
      | result        |
      | output        |
      | epoch         |
      | job           |
      | completed Run |
      | aborted build |

  @core-review
  Scenario: A retry cannot move a predeclared source to another node
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And another node is offered for that producer
    Then the original node and handoff remain authoritative

  @core-review
  Scenario: The daemon reservation is retained with the Run start
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And the selected daemon reserves the producer source
    Then reconnecting recovers that exact daemon-issued location

  @core-review
  Scenario: A replacement node cannot supply the reserved source
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And a replacement node answers the source reservation
    Then the source answer is refused without changing start admission
