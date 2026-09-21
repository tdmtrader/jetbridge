Feature: Cancellation cleanup discovery and retry progress are durable

  @core-review
  Scenario Outline: Real Run work is fairly discovered and claimed
    Given an internally admitted v2 result Run
    When cancellation cleanup bookkeeping exercises "<case>"
    Then cleanup progress is bounded and survives replacement
    Examples:
      | case                        |
      | exact discovery and replay  |
      | discovery bound             |
      | operation rotation          |
      | poison debt and restart     |
      | finite high-water           |
      | claim rollback              |
      | stale epoch                 |
      | stale attempt               |
      | claim deadline              |
      | safe residual deadline      |
      | expired lease progress      |
      | expired operation response  |
      | timeout debt                |
      | interrupted claim recovery  |
      | progress rollback           |
      | unknown debt                |
      | run rotation and bound      |
      | ordinary Run excluded       |
      | invalid limits              |
