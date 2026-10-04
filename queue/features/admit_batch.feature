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

  Scenario: Admitting the main branch itself is refused
    Given a queue whose main branch is "trunk"
    When a change on "trunk", "main" or "master" is admitted
    Then each is refused naming its branch
    And a change on "feature" is admitted

  Scenario: A change built on a merge of two queued changes is refused at admission with a clear reason
    Given changes "a" and "b" are queued and neither builds on the other
    When change "x" built on both "a" and "b" is admitted
    Then it is refused saying it builds on a merge of queued changes "a" and "b"
    And nothing is queued for "x"
