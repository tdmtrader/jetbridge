Feature: Remaining volume behavior over real PostgreSQL

  Scenario: A duplicate cache volume remains container-owned
    When remaining production volume behavior "duplicate-container" is exercised
    Then the remaining volume behavior is exact

  Scenario: An invalidated stream chain remains usable for an earlier build
    When remaining production volume behavior "stream-chain-before-invalidation" is exercised
    Then the remaining volume behavior is exact

  Scenario: An invalidated stream chain is unavailable to a later build
    When remaining production volume behavior "stream-chain-after-invalidation" is exercised
    Then the remaining volume behavior is exact

  Scenario: A stream from an invalid source remains container-owned without error
    When remaining production volume behavior "invalid-source-refused" is exercised
    Then the remaining volume behavior is exact

  Scenario: An invalidated source worker is returned to an earlier build
    When remaining production volume behavior "source-worker-before-invalidation" is exercised
    Then the remaining volume behavior is exact

  Scenario: An invalidated source worker is hidden from a later build
    When remaining production volume behavior "source-worker-after-invalidation" is exercised
    Then the remaining volume behavior is exact

  Scenario: A deleted source permits a replacement cache record
    When remaining production volume behavior "replacement-created" is exercised
    Then the remaining volume behavior is exact

  Scenario: A replacement cache volume becomes resource-owned
    When remaining production volume behavior "replacement-resource-type" is exercised
    Then the remaining volume behavior is exact

  Scenario: An earlier build finds the replacement cache volume
    When remaining production volume behavior "replacement-found-before" is exercised
    Then the remaining volume behavior is exact

  Scenario: An earlier build can inspect the invalidated streamed cache record
    When remaining production volume behavior "invalid-stream-retained-before" is exercised
    Then the remaining volume behavior is exact

  Scenario: A later build finds the replacement cache volume
    When remaining production volume behavior "replacement-found-after" is exercised
    Then the remaining volume behavior is exact

  Scenario: A later build can inspect the invalidated streamed cache record
    When remaining production volume behavior "invalid-stream-retained-after" is exercised
    Then the remaining volume behavior is exact

  Scenario: A local resource volume reports the nested resource type and versions
    When remaining production volume behavior "nested-resource-type" is exercised
    Then the remaining volume behavior is exact

  Scenario: A streamed resource volume reports the nested resource type and versions
    When remaining production volume behavior "streamed-resource-type" is exercised
    Then the remaining volume behavior is exact

  Scenario: Deleting a worker cascades its container volume
    When remaining production volume behavior "worker-delete-cascades" is exercised
    Then the remaining volume behavior is exact
