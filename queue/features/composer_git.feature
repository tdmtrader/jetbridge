Feature: Composing a batch with git
  Each change in a batch becomes one squashed commit, titled land(<id>) and
  naming its original commit, on top of main, in batch order. The result is
  pushed to the queue's own candidate branch; main is never touched.

  Scenario: Two independent changes compose into one squashed commit each, on top of main, in order
    Given main has one commit
    And changes "a" and "b" each add their own file
    When the batch "a", "b" is composed onto main
    Then the candidate is two commits on top of main, "land(a)" then "land(b)"
    And each commit names its original commit and none is a merge

  Scenario: A change that conflicts with an earlier one in the batch is named as the conflict
    Given changes "a" and "b" both rewrite the same line differently
    When the batch "a", "b" is composed onto main
    Then "b" is named as the change that does not merge

  Scenario: A stacked child after its parent composes cleanly
    Given change "c" is built on top of change "p"
    When the batch "p", "c" is composed onto main
    Then the candidate is "land(p)" then "land(c)" on top of main
    And composing "c" again onto that candidate adds no commit

  Scenario: The candidate branch is updated and main is untouched
    Given the candidate branch already points at an unrelated commit
    When the batch "a" is composed onto main
    Then the candidate branch points at the returned commit
    And main and every other branch are unchanged

  Scenario: A missing commit is an error, not a conflict
    Given change "a" names a commit the remote does not have
    When the batch "a" is composed onto main
    Then composing fails with no verdict and names no conflict
    And the candidate branch is not created

  Scenario: A stacked change that undoes its parent's change is composed faithfully
    Given change "p" adds a file and change "c", built on "p", deletes it
    When the batch "p", "c" is composed onto main
    Then the candidate does not have the file

  Scenario: a change whose parent already landed applies only its own changes
    Given change "p" adds a file and has already landed on main as a squash
    And change "c", built on "p", changes that file, or deletes it
    When the batch "c" is composed onto main
    Then the candidate has only "c"'s own changes on top of main

  Scenario: A compose hook's output is in the candidate commit
    Given a compose hook that writes a generated file
    When the batch "a" is composed onto main
    Then the candidate has the generated file
    And main is unchanged

  Scenario: A compose hook that fails gives no verdict and ejects nobody
    Given a compose hook that exits with status 3
    When the batch "a" is composed onto main
    Then composing fails with "compose hook failed: exit 3" and no conflict
    And the candidate branch is not created

  Scenario: A compose hook that changes paths outside hook_owned gives no verdict
    Given a compose hook that writes some files outside the paths it owns
    When the batch "a" is composed onto main
    Then composing fails naming how many paths are outside hook_owned, never their names, and no conflict
    And the candidate branch is not created

  Scenario: A compose hook that renames, deletes or links out of hook_owned, or writes an ignored file there, gives no verdict
    Given a compose hook that moves, removes, links out or ignores a file outside the paths it owns
    When the batch "a" is composed onto main
    Then composing fails naming one path outside hook_owned, and no conflict

  Scenario: A compose hook that runs too long leaves no process behind
    Given a compose hook that starts a background process and outlives the hook timeout
    When the batch "a" is composed onto main
    Then composing fails with "compose hook failed: timeout" and the background process is gone

  Scenario: A compose hook that runs too long gives no verdict
    Given a compose hook still running after the hook timeout
    When the batch "a" is composed onto main
    Then composing fails with "compose hook failed: timeout" and no conflict

  Scenario: With no compose hook the candidate is only the composed changes
    Given no compose hook is configured
    When the batch "a" is composed onto main
    Then the candidate is one commit on top of main, "land(a)"

  Scenario: Each land commit carries the rows block the old queue wrote, and its original line
    Given changes "a" and "b"
    When the batch "a", "b" is composed onto main
    Then each land commit names its change in a rows block the old queue's reader reads, after its original line
