Feature: Cancellation recovers only the original supervisor outcome

  @core-review
  Scenario Outline: Unprovable completion survives controller and daemon loss
    Given a Run with resource checks and a ready output node
    When its real worker admits a "cancel daemon loss with <fault>" execution
    Then cancellation preserves an unresolved execution

    Examples:
      | fault |
      | missing exit |
      | corrupt exit |
      | replacement Pod |
