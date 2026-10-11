Feature: An author withdraws a queued change
  The author asks by pushing a control ref naming the change and the commit
  they saw queued. The runner acts on it after admissions at the start of its
  next step and deletes the request once the queue is saved. A withdrawn change
  is neither ejected nor blamed, and its id may be admitted again.

  Scenario: A queued change is withdrawn without being ejected or blamed
    Given changes "a" and "b" are queued
    When the author asks to withdraw "a"
    And the runner takes its next step
    Then only "b" is queued and "a" is announced as withdrawn
    And "a" is kept as a withdrawn settle record, not an eject, and the request is deleted
    And "a" can be admitted again

  Scenario: A change in flight is withdrawn and its batch is recomposed without it
    Given changes "a" and "b" are queued and tested together
    When the author asks to withdraw "a"
    And the runner takes its next step
    Then "b" is tested alone and lands, and nothing is ejected

  Scenario: A change that another queued change builds on is not withdrawn
    Given change "b" is queued on change "a"
    When the author asks to withdraw "a"
    And the runner takes its next step
    Then both are still queued and the request is deleted

  Scenario: A withdraw request for a commit the change has left is deleted unheeded
    Given change "a" is queued
    When the author asks to withdraw "a" at an earlier commit
    And the runner takes its next step
    Then "a" is still queued and the request is deleted

  Scenario: queue withdraw asks the live runner to remove a queued change
    Given change "a" is queued and a runner holds the lease
    When the author runs queue withdraw for "a" and the runner takes its next step
    Then "a" is no longer queued, its waiting admit ref and the request are deleted
