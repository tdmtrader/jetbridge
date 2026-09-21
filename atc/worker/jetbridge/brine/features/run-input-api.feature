Feature: Uploading a local input through the authenticated public API

  Scenario Outline: An HTTP client receives only its authorized input grant
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "a valid source"
    Then its public input upload is requested by <caller>

    Examples:
      | caller              |
      | "owner"             |
      | "anonymous"         |
      | "viewer"            |
      | "cross-team"        |
      | "held"              |
      | "missing authority" |
      | "malformed archive" |
      | "unknown input"     |
