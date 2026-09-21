Feature: Any local client can inspect a detached review Run

  @core-review
  Scenario: A human CLI reads the retained observation after payload cleanup
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "owner" client reads the retained Run result
    And a fresh review CLI reads the "known" Run status
    And its disposable payload is reclaimed after a newer Run
    And a fresh review CLI reads the "known" Run status
    And a fresh review CLI reads the "unknown" Run status
    Then the header reader returns the retained result and version

  @core-review
  Scenario: The CLI distinguishes a pending review from a published result
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And a fresh "owner" client reads the retained Run result
    And a fresh review CLI reads the "known" Run status
    Then the aggregate Run remains running with no public result
  @core-review
  Scenario: An MCP client reads the retained observation after payload cleanup
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "owner" client reads the retained Run result
    And a fresh review MCP reads the "known" Run status
    And its disposable payload is reclaimed after a newer Run
    And a fresh review MCP reads the "known" Run status
    And a fresh review MCP reads the "unknown" Run status
    And a fresh "cross-team" client reads the retained Run result
    And a fresh review MCP reads the "redacted" Run status
    Then the header reader returns the retained result and version

  @core-review
  Scenario: The MCP distinguishes a pending review from a published result
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And a fresh "owner" client reads the retained Run result
    And a fresh review MCP reads the "known" Run status
    And a fresh review MCP reads the "invalid" Run status
    Then the aggregate Run remains running with no public result
