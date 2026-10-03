Feature: Admitting changes and forming a batch

  Scenario: Two admitted changes form one batch in order
    Given an empty queue
    When change "a" is admitted
    And change "b" is admitted
    Then the next batch holds "a" then "b"

  Scenario: An ejected change is never batched
    Given changes "a", "b" and "c" are admitted
    When change "b" is ejected
    Then the next batch holds "a" then "c"
    And admitting "b" again is refused because it was ejected
