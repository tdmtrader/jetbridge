Feature: A Run task receives its retained named inputs

  @core-review
  Scenario Outline: Task preparation uses the admitted routes and exact bindings
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "<binding>"
    Then its consuming task prepares with "<case>"

    Examples:
      | binding                       | case                 |
      | a valid source                | retained inputs      |
      | one source under two names    | retained inputs      |
      | one name routed to two slots  | retained inputs      |
      | a valid source                | changed route        |
      | a valid source                | changed input path   |
      | a valid source                | missing runtime slot |
      | a valid source                | prebound raw tree    |
      | a valid source                | wrong task           |
      | a valid source                | wrong team           |
      | a valid source                | wrong build          |
      | a valid source                | aborted build        |
      | a valid source                | disabled activation  |
      | a valid source                | edited template      |
      | a valid source                | repeated preparation |
