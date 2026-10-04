Feature: Keeping the queue's state and lease in git
  The queue's state and its single-owner lease live together in one ref on
  the repository, so every change to either is one compare-and-swap push.
  A save made from an old version, or under an older lease, never lands.

  Scenario: An empty repository loads as an empty queue
    Given a repository with no queue state
    When the queue state is loaded
    Then it is an empty queue with no version

  Scenario: State saved by one owner loads back identically
    Given an owner holds the lease
    When it saves a queue with entries, runs in flight and a landing
    Then loading returns exactly that queue at the saved version

  Scenario: A save from an old version is refused and the state is unchanged
    Given an owner has saved the queue twice
    When it saves again from the first version
    Then the save is refused as stale
    And the stored queue is still the second one

  Scenario: A save under an older lease is refused even when the version matches
    Given a second owner took over the expired lease of the first
    When the first owner saves from the current version
    Then the save is refused naming the newer lease
    And the stored queue is unchanged

  Scenario: A second owner cannot take a live lease but takes over an expired one with a higher token
    Given an owner holds a lease for one minute
    When a second owner asks for it straight away
    Then it is refused, naming the holder and when the lease expires
    When a second owner asks again after the lease expires
    Then it holds the lease with a higher token

  Scenario: The same owner renews its lease and keeps its token
    Given an owner holds the lease and has saved the queue
    When it renews the lease before it expires
    Then the token is unchanged and the lease runs later
    And the saved version still matches, so its next save succeeds
