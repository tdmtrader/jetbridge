Feature: Human and agent clients retrieve typed detached review findings

  @core-review @review-linux
  Scenario Outline: A fresh client retrieves the real worker report after cleanup
    Given a Run producer and a ready output node
    When its runtime producer publishes review worker findings
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "owner" client downloads the named Run result
    And a fresh review "<surface>" retrieves typed findings
    And its disposable payload is reclaimed after a newer Run
    And a fresh review "<surface>" retrieves typed findings
    Then the header reader returns the retained result and version

    Examples:
      | surface |
      | CLI     |
      | MCP     |
