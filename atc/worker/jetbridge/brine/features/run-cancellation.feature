Feature: Whole-Run cancellation durably fences later work

  @core-review
  Scenario: Acceptance records the first requester without aborting builds
    Given an internally admitted v2 result Run
    When whole-Run cancellation is requested by "first-owner" for "café"
    Then the Run cancellation outcome is "accepted"
    And cancellation records its first request without stopping a build
    When whole-Run cancellation is repeated by "later-owner" for "a different reason"
    Then the Run cancellation outcome is "already_requested"
    And the original cancellation facts are unchanged

  @core-review
  Scenario: Cancellation rollback leaves neither facts nor a fence
    Given an internally admitted v2 result Run
    When the whole-Run cancellation transaction is rolled back
    Then the Run has no cancellation request and still admits work

  @core-review
  Scenario Outline: Cancellation closes every later admission path
    Given an internally admitted v2 result Run
    When whole-Run cancellation is requested by "first-owner" for "stop this run"
    And its cancelled Run attempts "<operation>"
    Then that cancelled Run operation is refused without admitting work
    Examples:
      | operation      |
      | manual build   |
      | rerun          |
      | build start    |
      | output start   |
      | scheduler debt |
      | unpause        |

  @core-review
  Scenario Outline: Invalid reasons never persist or leak through errors
    Given an internally admitted v2 result Run
    When cancellation supplies an invalid "<reason>" reason
    Then cancellation rejects the reason without storing or repeating it
    Examples:
      | reason        |
      | empty         |
      | too long      |
      | control       |
      | invalid UTF-8 |

  @core-review
  Scenario: Cancellation normalizes its optional reason
    Given an internally admitted v2 result Run
    When whole-Run cancellation is requested by "first-owner" for "café"
    Then the Run cancellation outcome is "accepted"
    And its cancellation reason is normalized to "café"

  @core-review
  Scenario: Acceptance prevents ordinary terminal publication
    Given an internally admitted v2 result Run
    When its uncaptured result producer finishes successfully
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the otherwise complete Run is cancelled
    And the aggregate Run completion is attempted
    Then the aggregate Run remains running with no public result

  @core-review
  Scenario: Completion before cancellation keeps the published observation
    Given an internally admitted v2 result Run
    When its uncaptured result producer finishes successfully
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And the completed Run receives a cancellation request
    Then cancellation reports already terminal without changing the observation
