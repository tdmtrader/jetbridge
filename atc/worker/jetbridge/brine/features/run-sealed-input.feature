Feature: Sealed input grants authorize first admission without becoming retained credentials

  @core-review
  Scenario Outline: Sealed sources are scoped and replayed by stable identity
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "a valid source"
    Then its input invocation receives a sealed source with "<case>"

    Examples:
      | case |
      | valid grant |
      | task delivery |
      | missing authority |
      | wrong epoch |
      | wrong team |
      | wrong template |
      | wrong principal |
      | wrong input |
      | unpublished generation |
      | expired grant |
      | tampered grant |
      | wrong source identity |
      | expired replay |
      | missing bearer replay |
      | changed bearer replay |
      | changed source identity replay |
      | missing source identity replay |
      | rollback then expired |
