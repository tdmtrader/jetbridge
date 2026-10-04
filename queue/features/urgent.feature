Feature: The urgent lane

  Scenario: An urgent change goes to the front of the next batch
    Given changes "a", "b" and "c" are queued
    When change "c" is marked urgent
    Then the next batch holds "c" then "a" then "b"

  Scenario: Urgent changes keep their admission order among themselves
    Given changes "a", "b", "c" and "d" are queued
    When change "d" and then change "b" are marked urgent
    Then the next batch holds "b" then "d" then "a" then "c"

  Scenario: An urgent change never skips the queued change it builds on
    Given changes "a", "b" and "c" are queued and "c" builds on "b"
    When change "c" is marked urgent
    Then the next batch holds "b" then "c" then "a"

  Scenario: Only a queued change can be marked urgent
    Given change "a" is queued and change "z" was never admitted
    When change "a" is ejected and then marked urgent
    Then it is refused because it was ejected
    And marking "z" urgent is refused
