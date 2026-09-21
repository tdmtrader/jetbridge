@review-linux @core-review
Feature: Run cancellation interrupts the original supervised command

  Scenario Outline: Cancellation settles an active execution without removing its source
    Given a Run producer and a ready output node
    When its real worker admits a "<case>" execution
    Then cancellation closes only its exact execution

    Examples:
      | case                       |
      | cancel active              |
      | cancel active ignores TERM |
