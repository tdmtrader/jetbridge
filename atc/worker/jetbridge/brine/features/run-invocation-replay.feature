Feature: Versioned Run admission owns one scoped invocation identity

  @core-review
  Scenario Outline: Replays preserve caller intent and authorization
    Given a versioned invocation of a parameterized template
    When its caller submits with "<case>"
    Then the scoped invocation outcome is correct

    Examples:
      | case |
      | an unchanged replay |
      | a changed parameter |
      | an explicitly supplied default |
      | a changed template default |
      | a paused template |
      | a different principal |
      | a different key |
      | revoked team membership |
      | a rolled back first transaction |
      | concurrent requests |
      | an explicit null |
      | a removed template parameter |
      | a maximum length key |
      | a replay callback |
      | a failed first callback |
      | retained record mutation |

  @core-review
  Scenario Outline: Invalid invocation keys create nothing
    Given a versioned invocation of a parameterized template
    When its caller submits with "<case>"
    Then the scoped invocation outcome is correct

    Examples:
      | case |
      | an empty key |
      | an oversized key |
      | a key containing spaces |
      | a non-ASCII key |
      | no stable principal |
      | a stronger v2 capability |
      | a weakened v2 capability |
      | disabled activation |
      | unsupported causation |
