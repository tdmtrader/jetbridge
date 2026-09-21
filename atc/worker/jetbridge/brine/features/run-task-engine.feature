Feature: The task engine prepares retained Run inputs through the real worker

  @core-review
  Scenario Outline: The engine admits only retained task routes
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "<binding>"
    Then its real task engine prepares inputs with "<case>"

    Examples:
      | binding                      | case                   |
      | a valid source               | live                   |
      | an input and a result        | live                   |
      | one source under two names   | live                   |
      | one name routed to two slots | live                   |
      | a valid source               | missing admission port |
      | a valid source               | changed route          |
      | a valid source               | changed task           |
      | a valid source               | aborted build          |
