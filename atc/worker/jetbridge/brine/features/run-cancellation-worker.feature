Feature: Cancellation cleanup survives worker replacement

  @core-review
  Scenario Outline: Worker ownership is durable, bounded and fenced
    Given an internally admitted v2 result Run
    When cancellation ownership exercises "<case>"
    Then the cancellation lease preserves exclusive database-clock ownership
    Examples:
      | case                    |
      | first claim             |
      | competing owner         |
      | racing owners           |
      | live owner reclaim      |
      | rollback                |
      | takeover                |
      | same owner after expiry |
      | expired lease use       |
      | lease use blocked past expiry |
      | claim blocked past expiry   |
