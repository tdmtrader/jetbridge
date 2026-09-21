Feature: Exact capture admission survives retries without changing its identity

  @core-review @HOP-3 @HOP-4
  Scenario: A restarted controller recovers the daemon's pre-start hold
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller reconnects before the source hold was recorded
    Then the held source is recovered without authorizing capture

  @core-review @HOP-3 @HOP-4
  Scenario Outline: A restarted controller requires the pinned control key
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller recovers the source using "<keys>"
    Then unverifiable hold recovery is refused without changing durable state

    Examples:
      | keys |
      | the receipt key |
      | an unknown epoch |
      | no keys |
      | duplicate epochs |
      | a malformed public key |

  @core-review @HOP-3 @HOP-4
  Scenario: Key rotation retains the ability to recover an older hold
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller recovers the source using "a retained epoch"
    Then the held source is recovered without authorizing capture

  @core-review @HOP-1 @HOP-3
  Scenario: Retrying a predeclaration cannot change its capture deadline
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller retries the predeclaration with a different deadline
    Then the mismatched admission is refused without changing durable state

  @core-review @HOP-1 @HOP-3
  Scenario Outline: A source reservation must match the whole admitted identity
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller records a reservation with a different "<field>"
    Then the mismatched admission is refused without changing durable state

    Examples:
      | field |
      | fence |
      | epoch |
      | output |

  @core-review @HOP-1 @HOP-3
  Scenario Outline: A source hold must match the whole reserved identity
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller records a hold with a different "<field>"
    Then the mismatched admission is refused without changing durable state

    Examples:
      | field |
      | fence |
      | epoch |
      | output |
      | generation |
      | node |

  @core-review @HOP-1 @HOP-3
  Scenario: Identical admission retries preserve the acknowledged source
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the controller repeats the exact admission after reconnecting
    Then the original source reservation and hold remain acknowledged
