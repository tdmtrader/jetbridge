Feature: Reading the queue's entries from the command line

  Scenario: The list shows queued changes in order with what they build on
    Given two queued changes where the second builds on the first
    When the operator lists the queue
    Then each change is on its own line, in admission order, with its commit and what it builds on

  Scenario: The ejected list shows recent ejects with why, cause and when
    Given two changes ejected at different times
    When the operator lists the ejected changes
    Then the newest eject is first with its reason, cause and time

  Scenario: Explain shows one change's admit, runs, settle records and refusals
    Given a change that was refused once, then admitted, run and ejected
    When the operator explains that change
    Then its admit, run, settle record and refusal are all shown

  Scenario: Explaining an unknown change fails clearly
    Given a queue that has never seen the change "nope"
    When the operator explains "nope"
    Then the command fails saying there is no such entry
