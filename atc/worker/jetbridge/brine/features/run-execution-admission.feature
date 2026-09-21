Feature: Every Run-owned executor has a recoverable exact identity

  @core-review
  Scenario Outline: Execution admission preserves ownership and the cancellation fence
    Given a v2 result Run with resource checks
    When Run execution admission exercises "<case>"
    Then its execution ownership is durable and fenced

    Examples:
      | case                         |
      | job replay                   |
      | check ownership              |
      | concurrent admission         |
      | rollback                     |
      | cancellation first           |
      | cancelled replay             |
      | immutable node               |
      | capture identity             |
      | cancellation discovery       |
