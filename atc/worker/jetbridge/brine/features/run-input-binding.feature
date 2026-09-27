Feature: Versioned admission retains authorized named input claims

  @core-review
  Scenario Outline: Inputs resolve from authorized immutable Run results
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "<case>"
    Then the named input admission outcome is correct

    Examples:
      | case |
      | a valid source |
      | no required input |
      | an undeclared input |
      | an unknown source |
      | an unknown result |
      | a foreign team |
      | a raw tree ref |
      | an unchanged replay |
      | a changed source on replay |
      | a rolled back admission |
      | a generic claim release |
      | one source under two names |
      | one name routed to two slots |
      | one name routed to two tasks |
      | immutable input bindings |
      | a reclaimed payload |
