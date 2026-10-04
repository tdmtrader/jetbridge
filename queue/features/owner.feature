Feature: Every entry records its owner
  A change is admitted by a person. The queue keeps that name on the entry,
  in its settle record and in the notice of an eject, so the right person is told.
  Only the name is kept, never an email address.

  Scenario: An admitted change keeps its owner through the eject record and the notice
    Given a change admitted by alice that fails its tests alone
    When the queue ejects it
    Then the settle record and the eject notice name alice

  Scenario: A change is owned by the author of its commit
    Given a change whose commit was written by alice
    When the queue reads the admission
    Then the pending change is owned by alice and no email address is kept

  Scenario: The log notifier writes the owner of each change
    Given a log notifier writing to a log
    When the queue announces an eject of a change owned by alice
    Then the log line names alice as the owner
