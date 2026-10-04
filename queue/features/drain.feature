Feature: Drain lists in-flight work for a safe rollback
  Before the queue is replaced or rolled back, an operator asks it, read-only,
  which changes are in flight, whether a land is being pushed, and who drives it.
  The answer is printed whole or not at all.

  Scenario: An empty queue drains to an idle push and no lease
    Given a queue with no changes
    When I drain the queue
    Then it prints only that the push is idle and that no one holds the lease

  Scenario: Drain lists the rows in flight before the queued ones, in queue order
    Given a queue with two changes in flight and two queued
    When I drain the queue
    Then it lists the two in flight first and then the queued ones in queue order

  Scenario: Drain says a push is mid-flight while a land is in progress
    Given a queue whose land of a change is in progress
    When I drain the queue
    Then it lists that change as in flight and says the push is mid-flight

  Scenario: Drain names the lease holder and the run it drives, and no holder once it expired
    Given a queue whose driver holds the lease while a run is in flight
    When I drain the queue
    Then it names the driver and the run, and no holder once the lease has expired

  Scenario: An unreadable store drains nothing and fails
    Given a queue whose state cannot be read
    When I drain the queue
    Then it fails and prints nothing
