Feature: Admitting changes through the existing request refs
  A queue switched on beside the old one reads the rows the old admit pushes,
  so nobody has to change how they admit. The old rows stay where the old
  queue reads them until the queue settles each one; then the queue removes
  it exactly as the old queue would, so the old queue, run again, never lands
  it twice. The queue keeps its own refs elsewhere and writes nothing else there.

  Scenario: The existing request refs are read only when named, and never where the queue keeps its own refs
    Given a config that names the existing request refs
    When the queue's own refs default to a place inside them
    Then the config is refused, naming the keys to move

  Scenario: The flip config parses, with every section its adapters read
    When the flip config is read with the repository given by the resource
    Then it names the existing request refs and keeps the queue's own refs apart
    And the runner and notify sections parse

  Scenario: A change admitted through the existing request refs is queued and lands
    Given a row pushed by the old admit
    When the runner takes its steps
    Then the change is queued, its row kept while it waits
    And it lands with the rows block the old queue reads

  Scenario: Once it lands, its request ref is removed as the old queue removes a landed row
    Given a row pushed by the old admit has landed
    When the runner takes its next step
    Then the row's ref is gone and nothing else is written

  Scenario: An eject writes the old queue's eject record and removes the request ref, in one push
    Given a row pushed by the old admit fails its test
    When the runner takes its next step
    Then the old queue's eject record holds the reason and the shipper
    And the row's ref is gone
    And the old admit of the same row id at a new commit queues it again

  Scenario: A withdraw or a supersede on the existing request refs removes the queued change
    Given two rows pushed by the old admit are queued
    When the old queue withdraws one and supersedes the other
    Then neither is queued and the old records are left as written

  Scenario: A withdraw through the queue retires the request ref as the old queue's withdraw does
    Given a row pushed by the old admit is queued
    When it is withdrawn through the queue
    Then the old queue's withdraw record is written and the row's ref is gone

  Scenario: The queue's own admit still works beside the existing request refs
    When one change is admitted the queue's own way and one the old way
    Then both are queued and only the queue's own admit ref is deleted

  Scenario: The queue writes nothing under the existing request refs but the old queue's own retirements
    Given rows pushed by the old admit, one ejected and one landed
    Then the only ref left there is the eject record
