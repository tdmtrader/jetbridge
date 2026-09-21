Feature: Run output failure stays pending until exact source release

  @core-review
  Scenario: A failed producer can finish after its release is retained
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And its Run controller records the daemon source release
    And its build finishes with the "failed" outcome
    Then the Run retains the release and the build is complete

  @core-review
  Scenario: A cancelled producer can finish after releasing its source
    Given a Run producer with an authoritative "success" finish
    When its Run controller records producer cancellation
    And its Run controller records the daemon source release
    And its build finishes with the "aborted" outcome
    Then the Run retains the release and the build is complete

  @core-review
  Scenario: A cancelled capture keeps its checkpoint while its build finishes
    Given a Run producer with an authoritative "success" finish
    When its Run controller commits capture selection
    And its Run controller records producer cancellation
    And its Run controller records the daemon source release
    And its build finishes with the "aborted" outcome
    Then the Run retains the release and the build is complete

  @core-review
  Scenario: A release answer can be repeated after the build finishes
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And its Run controller records the daemon source release
    And its build finishes with the "failed" outcome
    And another controller records the same source release
    Then the Run retains the release and the build is complete

  @core-review
  Scenario: A release commit can roll back after the daemon has released
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And its Run release transaction rolls back
    Then the Run still owes release acknowledgement
    When another controller records the same source release
    And its build finishes with the "failed" outcome
    Then the Run retains the release and the build is complete

  @core-review
  Scenario Outline: A release acknowledgement must match the exact fenced source
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And its Run controller offers a different "<fact>" for source release
    Then Run source release is refused and its build remains pending
    Examples:
      | fact        |
      | signature   |
      | epoch       |
      | source hold|
      | incarnation |

  @core-review
  Scenario: A released failure cannot be reported as successful
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And its Run controller records the daemon source release
    And its build finishes with the "succeeded" outcome
    Then the build outcome is refused and remains pending

  @core-review
  Scenario: Cancellation before source reservation owes no daemon release
    Given a Run producer with an authoritative "not reserved" finish
    When its Run controller records producer cancellation
    And its build finishes with the "aborted" outcome
    Then the unreserved producer is complete without release evidence

  @core-review
  Scenario: Generic source release cannot bypass the owning Run transaction
    Given a Run producer with an authoritative "failure" finish
    When its Run controller commits the no-capture decision
    And a generic source-release caller bypasses the Run acknowledgement
    Then Run source release is refused and its build remains pending
