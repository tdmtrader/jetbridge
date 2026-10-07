Feature: A Run owns admission of its exact output producer

  @core-review
  Scenario: Concurrent controllers recover one admitted execution
    Given an internally admitted v2 result Run
    When two controllers concurrently admit the producer
    Then one pending capture belongs to that exact Run and build

  @core-review
  Scenario: A pending capture prevents externally completing its build
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And its build tries to finish before its capture settles
    Then the build remains unfinished and the capture is retained

  @core-review
  Scenario: A capture link cannot be removed to permit another execution
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And deletion of its capture link is attempted
    Then the original capture link remains immutable

  @core-review
  Scenario: Every Run is born under the one v2 contract and cannot leave it
    Given a Run admitted from a parameterized template
    Then the Run records an immutable v2 birth contract

  @core-review
  Scenario Outline: V2 admission requires the current activated contract
    Given internal v2 admission with "<activation>" activation
    Then v2 admission is refused before allocating a Run
    Examples:
      | activation |
      | disabled   |
      | stale      |

  @core-review
  Scenario: Starting a result producer inserts exactly one pending capture
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    Then one pending capture belongs to that exact Run and build

  @core-review
  Scenario: Reconnecting repeats the original producer admission
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And a new controller repeats the producer admission
    Then one pending capture belongs to that exact Run and build

  @core-review
  Scenario: Rolling back start admission leaves no orphan capture
    Given an internally admitted v2 result Run
    When its producer admission transaction is rolled back
    Then neither a capture link nor a capture row remains

  # The control-key generation is not a fact of the Run: a capture is admitted
  # under the generation this control plane speaks for now, and the database
  # holds no generation to compare a different one against. A step admitted
  # under another nonzero generation is refused where the capabilities would be
  # used -- the pod build ("A ready label without a matching control epoch
  # admits nothing"). What start admission does refuse is no generation at all:
  # a control plane with no output plane configured.
  @core-review
  Scenario Outline: Unadmitted producer facts cannot select an output
    Given an internally admitted v2 result Run
    When its producer asks to start with a different "<fact>"
    Then start admission is refused without a capture
    Examples:
      | fact          |
      | task          |
      | result        |
      | output        |
      | no generation |
      | job           |
      | completed Run |
      | aborted build |

  @core-review
  Scenario: A retry cannot move a pending capture to another node
    Given an internally admitted v2 result Run
    When its result producer requests start admission
    And another node is offered for that producer
    Then the original node and capture remain authoritative
