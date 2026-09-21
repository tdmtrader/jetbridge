Feature: The worker delivers each retained Run input under an exact lease

  @core-review
  Scenario Outline: The actual container receives only admitted managed inputs
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "<binding>"
    Then its real worker delivers inputs with "<case>"

    Examples:
      | binding                      | case                  |
      | a valid source               | live                  |
      | one source under two names   | live                  |
      | one name routed to two slots | live                  |
      | a valid source               | changed ref           |
      | a valid source               | changed name          |
      | a valid source               | missing slot          |
      | a valid source               | missing task identity |
      | a valid source               | aborted build         |
