Feature: A new commit for a queued change replaces it in place
  The author pushes the fixed commit under the same id. While the change is
  still queued it is replaced where it stands, so it keeps its position, and
  anything in flight is recomposed with the new commit. A landed or ejected id
  still refuses a new commit; an ejected one is resolved first.

  Scenario: A new commit under a queued id replaces it and keeps its position
    Given changes "a", "b" and "c" are queued
    When a new commit is pushed for admission under "b"
    And the runner takes its next step
    Then the queue is still "a", "b", "c" and "b" is at the new commit
    And "b" is announced as superseded, its pushed ref is deleted and nothing is refused

  Scenario: A change in flight is recomposed with the new commit
    Given changes "a" and "b" are queued and tested together
    When a new commit is pushed for admission under "a"
    And the runner takes its next step
    Then "a" and "b" are tested again with "a" at the new commit and nothing is ejected

  Scenario: A new commit under an id that another queued change builds on is refused
    Given change "b" is queued on change "a"
    When a new commit is pushed for admission under "a"
    And the runner takes its next step
    Then "a" stays at its commit and the new one is refused with the reason

  Scenario: A new commit that merges two unrelated queued changes does not replace the queued one
    Given changes "a" and "b" are queued and neither builds on the other, and so is "c"
    When a new commit that merges "a" and "b" is pushed for admission under "c"
    And the runner takes its next step
    Then "c" stays at its commit and the new one is refused as divergent

  Scenario: A new commit built on the queued one does not make the change build on itself
    Given change "a" is queued
    When a new commit on top of it is pushed for admission under "a"
    Then it builds on no queued change
