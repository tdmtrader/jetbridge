Feature: Landing with git
  The git lander fast-forwards main to a tested candidate. Every call writes
  a new, higher fence to a lease ref on the remote; main and the lease ref
  move together in one atomic push, each only from the value it was read at.
  So once a newer call has written its fence, no older landing can move main.

  Scenario: A candidate ahead of main lands and moves the lease
    Given a remote whose main is behind a candidate
    When the candidate is landed with fence 5
    Then main is the candidate
    And the remote's lease holds fence 5

  Scenario: A candidate that is not ahead of main is refused
    Given a remote whose main has moved past where a candidate branched
    When the candidate is landed
    Then the landing is refused
    And it is named as main having moved
    And main does not move

  Scenario: A landing with an older fence is refused and main does not move
    Given a candidate was landed with fence 7
    When a newer candidate is landed with fence 6
    Then the landing is refused naming fence 7
    And main is still the first candidate

  Scenario: A landing still in flight with an old fence cannot land after a newer check fenced it
    Given two landers share one remote
    When the second checks main with fence 9
    And the first then lands a candidate with fence 8
    Then the landing is refused
    And main does not move

  Scenario: Main moving under the lander refuses the push and the lease does not move
    Given the remote's lease holds fence 1
    When main moves after the lander read it but before it pushes
    Then the landing is refused
    And main is where the other push left it
    And the remote's lease still holds fence 1

  Scenario: Contains reports a landed candidate
    Given a candidate was landed with fence 5
    When main is checked for it with fence 6
    Then it is reported as held
    And a candidate that never landed is reported as not held

  Scenario: a malformed lease is refused, never read as zero
    Given the remote's lease holds a message that is not exactly "fence" and a decimal number
    When a candidate is landed or main is checked
    Then it is refused naming the lease
    And main does not move

  Scenario: A credential in a git error is hidden
    Given a remote that refuses a push and names a URL with a password
    When a candidate is landed, or the state is saved
    Then the error shows the URL with the password hidden

  Scenario: Two git calls at once do not share a connection
    Given a remote reached over ssh
    When two git calls run at once
    Then neither uses an ssh control connection, whatever ssh options the user set

  Scenario: The lander reads where main points now
    Given a remote whose main points at a commit
    When main is moved and read again
    Then the lander reads the commit main points at now
    And a branch that does not exist is refused, named

  Scenario: A batch composes on main's sha, not on wherever the branch points later
    Given main's sha was read and main then moved
    When a batch is composed on the sha that was read
    Then the candidate sits on that sha
