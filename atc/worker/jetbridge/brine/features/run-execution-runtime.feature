Feature: Run ownership is enforced at the real worker boundary

  @core-review
  Scenario Outline: Container-based Run work uses one exact admission
    Given a Run with resource checks and a ready output node
    When its real worker admits a "<kind>" execution
    Then that worker preserves exact execution ownership and placement

    Examples:
      | kind |
      | task |
      | get  |
      | put  |
      | check |

  @core-review
  Scenario: A rejected finish commit is recovered without repeating the command
    Given a Run with resource checks and a ready output node
    When its real worker admits a "finish commit failure" execution
    Then that worker retains its signed execution witnesses

  @core-review
  Scenario: A node signature outside the configured trust set cannot start a command
    Given a Run with resource checks and a ready output node
    When its real worker admits a "untrusted signer" execution
    Then that worker refuses an unadmitted command

  @core-review
  Scenario Outline: Cancellation reconciles an execution without an output handoff
    Given a Run with resource checks and a ready output node
    When its real worker admits a "cancel <state>" execution
    Then cancellation closes only its exact execution

    Examples:
      | state |
      | prepared |
      | completed |
      | check completed |
      | unrecorded finish |
      | daemon loss |
      | unretained start |
      | aborted unretained start |

  @core-review
  Scenario: Cancellation closes a prepared container before its Pod exists
    Given a Run with resource checks and a ready output node
    When its real worker starts a prepared container after cancellation
    Then that worker refuses the late process without creating a Pod

  @core-review
  Scenario Outline: An existing Pod cannot bypass the cancellation fence
    Given a Run with resource checks and a ready output node
    When its real worker attempts "<operation>" after cancellation
    Then that worker refuses an unadmitted command

    Examples:
      | operation |
      | late launch |
      | intercept |

  @core-review
  Scenario Outline: The Run retains the node's exact start and finish evidence
    Given a Run with resource checks and a ready output node
    When its real worker admits a "witness <kind>" execution
    Then that worker retains its signed execution witnesses

    Examples:
      | kind |
      | task |
      | get |
      | put |
      | check |
