Feature: An authorized client downloads a retained Run result

  @core-review
  Scenario Outline: Named result downloads survive payload reclamation
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And a fresh "<caller>" client downloads the named Run result
    And its disposable payload is reclaimed after a newer Run
    And a fresh "<caller>" client downloads the named Run result
    Then the header reader returns the retained result and version

    Examples:
      | caller     |
      | owner      |
      | viewer     |
      | anonymous  |
      | cross-team |
      | unknown    |

  @core-review
  Scenario: A candidate cannot be downloaded before terminal publication
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And a fresh "owner" client downloads the named Run result
    Then the aggregate Run remains running with no public result
