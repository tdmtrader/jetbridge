Feature: An operator resolves an eject so the change can enter again
  An ejected id is refused for good, so a fixed change cannot reuse it. The
  operator asks by pushing a control ref naming the id and the commit that was
  ejected. The runner clears the eject, keeps a resolved settle record and
  deletes the request once the queue is saved; the id can then be admitted.

  Scenario: An ejected change is resolved and its id can be admitted again
    Given change "e" is ejected
    When the operator asks to resolve "e"
    And the runner takes its next step
    Then "e" is no longer ejected and is announced as resolved
    And "e" is kept as a resolved settle record and the request is deleted
    And a new commit pushed for "e" is queued

  Scenario: A resolve request for a change that is not ejected is deleted unheeded
    Given change "a" is queued
    When the operator asks to resolve "a"
    And the runner takes its next step
    Then "a" is still queued, nothing is announced and the request is deleted

  Scenario: A resolve request for a commit the eject has left is deleted unheeded
    Given change "e" is ejected
    When the operator asks to resolve "e" at an earlier commit
    And the runner takes its next step
    Then "e" is still ejected and the request is deleted

  Scenario: queue resolve asks the live runner to clear an eject
    Given change "e" is ejected and a runner holds the lease
    When the operator runs queue resolve for "e" and the runner takes its next step
    Then "e" is no longer ejected, the request is deleted and "e" can be admitted
