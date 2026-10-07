Feature: Run cancellation covers checks and output races

  @core-review
  Scenario Outline: Resource checks cannot cross the Run fence
    Given a v2 result Run with resource checks
    When whole-Run cancellation is requested by "first-owner" for "stop"
    And the cancelled Run requests a "<kind>" resource check
    Then no check is admitted into the cancelled Run
    Examples:
      | kind      |
      | persisted |
      | in-memory |
      | scanner   |
      | type      |

  @core-review
  Scenario Outline: Admitted checks keep durable Run identity
    Given a v2 result Run with resource checks
    When the running Run requests a "<kind>" resource check
    Then the check is durably owned by its Run without job result identity
    Examples:
      | kind      |
      | persisted |
      | in-memory |
      | scanner   |
      | type      |

  @core-review
  Scenario: Cancellation after publication keeps the capture's claim until finalization
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And the whole Run is cancelled after publication
    Then one Run capture claim protects that exact generation

  @core-review
  Scenario: Direct writes cannot rewrite the first cancellation request
    Given an internally admitted v2 result Run
    When whole-Run cancellation is requested by "first-owner" for "private reason"
    Then direct changes cannot remove or replace the cancellation request

  @core-review
  Scenario: Checks block completion but do not determine the Run verdict
    Given a v2 result Run with resource checks
    When the running Run requests a "scanner" resource check
    And all its Run jobs fail while the check remains active
    Then that active check prevents terminal Run publication
    When its owned resource check finishes with an error
    Then only the Run jobs determine the terminal outcome
