Feature: Run source reservation survives an interrupted reply

  @core-review
  Scenario: Cancellation waits for an admitted reservation attempt to reconcile
    Given a Run producer with an authoritative "not reserved" finish
    When its Run records intent to reserve the source
    And its producer build is aborted
    And its Run controller tries to settle that cancellation
    Then cancellation remains pending until the reservation attempt is resolved

  @core-review
  Scenario: A daemon reservation is recovered after cancellation
    Given a Run producer with an authoritative "not reserved" finish
    When its Run records intent to reserve the source
    And the daemon reserves the source without a database acknowledgement
    And its producer build is aborted
    And another controller records the reserved source
    And its Run controller records producer cancellation
    And its Run controller records the daemon source release
    And its build finishes with the "aborted" outcome
    Then the Run retains the release and the build is complete

  @core-review
  Scenario: Cancellation prevents dispatching a new reservation attempt
    Given a Run producer with an authoritative "not reserved" finish
    When its producer build is aborted
    And its Run tries to record intent to reserve the source
    Then the reservation attempt is refused without dispatch authority

  @core-review
  Scenario: A source reply without a durable reservation attempt is refused
    Given a Run producer with an authoritative "not reserved" finish
    When the daemon reserves the source without a database acknowledgement
    And another controller tries to record the reserved source
    Then the unadmitted source reply is refused
