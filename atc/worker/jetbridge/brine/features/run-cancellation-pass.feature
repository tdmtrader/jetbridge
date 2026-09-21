Feature: Cancellation makes bounded progress outside its claim transaction

  @core-review
  Scenario Outline: A real worker closes existing scheduler debt
    Given an internally admitted v2 result Run
    When the cancellation worker exercises "<case>" with real scheduler actions
    Then the worker advances only owned and claimed scheduling work
    Examples:
      | case                   |
      | ordinary Run           |
      | accepted cancellation  |
      | a competing worker     |
      | one connection         |
      | repeated pass          |
      | over 100 operations    |
      | over 50 Runs           |
