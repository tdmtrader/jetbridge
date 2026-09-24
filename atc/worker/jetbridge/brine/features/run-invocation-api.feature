Feature: Authenticated versioned Run admission over HTTP

  Scenario Outline: Public admission preserves ownership, replay and activation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "a valid source"
    Then its versioned HTTP invocation encounters <case>

    Examples:
      | case                         |
      | "accepted"                   |
      | "response loss replay"       |
      | "replay without bearer"      |
      | "replay archived template"   |
      | "changed input replay"       |
      | "foreign grant"              |
      | "invalid key"                |
      | "unknown field"              |
      | "trailing JSON"              |
      | "anonymous"                  |
      | "viewer"                     |
      | "cross-team"                 |
      | "operator hold"              |
      | "database hold"              |
      | "operator hold replay"       |
      | "database hold replay"       |
      | "paused template"            |
      | "custom create role"         |
      | "shared client"              |
      | "shared client replay"       |
