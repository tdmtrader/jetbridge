Feature: Admitting changes by pushing them, drained by the runner
  A change is admitted by pushing its commit under the admission prefix with
  its id. The runner admits what it finds there at the start of each step,
  under its own lease, and deletes each pushed ref only once it is saved.
  A change builds on every queued change whose commit is an ancestor of its
  own; nobody has to say so.

  Scenario: A change pushed while the runner holds the lease is queued on its next step
    Given a runner holds the queue's lease
    When change "a" is pushed for admission
    And the runner takes its next step
    Then "a" is queued, stamped with the runner's clock
    And its pushed ref is deleted
    And the runner kept its lease

  Scenario: A stacked change pushed after its parent builds on it and is batched with it
    Given change "p" is queued
    When change "child", whose commit sits on "p", is pushed for admission
    Then "child" builds on "p"
    And "p" and "child" form one batch, "p" first

  Scenario: A divergent merge is refused at drain, its admit ref deleted and announced, and the queue keeps going
    Given changes "a" and "b" are queued and neither builds on the other
    When change "x", a merge of "a" and "b", is pushed for admission
    And change "y" on "a" is pushed for admission
    And the runner takes its next step
    Then "x" is not queued and its pushed ref is deleted
    And "x" is announced as refused with the reason, which status keeps
    And "y" is queued

  Scenario: A crash after the save and before the admit ref is deleted admits the change once
    Given change "a" is pushed for admission
    When the runner saves "a" as queued and dies before deleting its pushed ref
    And another runner takes the next step
    Then "a" is queued once and its pushed ref is deleted

  Scenario: An unsafe change id is refused
    When a change is admitted with an id that is not one plain ref name
    Then it is refused and nothing is pushed
    And one pushed under the prefix anyway is refused at drain

  Scenario: A change already landed or ejected has its admit ref deleted and is not queued again
    Given change "a" has landed and change "b" was ejected
    When "a" and "b" are pushed for admission again
    And the runner takes its next step
    Then neither is queued and both pushed refs are deleted

  Scenario: Two changes admitted in the same second keep their order
    When change "b" and then change "a" are admitted in the same second
    Then the runner sees "b" before "a"
    And each pushed ref is deleted once its change is saved

  Scenario: A change updated after its test is not ejected
    Given change "a" was seen at one commit
    When "a" is admitted again at another commit before the first ref is deleted
    Then deleting the first ref leaves the new one in place

  Scenario: Settling an already settled change again changes nothing
    Given change "a" was seen and its pushed ref deleted
    When the same deletion is asked for again
    Then it succeeds and nothing changes
