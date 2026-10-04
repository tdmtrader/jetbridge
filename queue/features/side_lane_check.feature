Feature: Side lane: every eject is checked again just before it is made
  A red settles nothing until it is checked at the moment of use: main is read
  again just before the eject, and the change must have been tested with
  exactly the changes that would land before it now. A failed check is a
  recompose, never a verdict: nothing is ejected and no retry is spent. A
  recompose is kept as one settle record for each time it happens.

  Scenario: A red whose main moves just before the eject is recomposed, not ejected
    Given change "a" is being tested on main
    When it fails and main moves before "a" is ejected
    Then nothing is ejected and "a" is recorded as a recompose naming the old and the new main
    And "a" is ejected only once it fails on the new main

  Scenario: A red tested without a change that would land before it is recomposed, not ejected
    Given changes "a" and "c" are queued
    When "c" is tested without "a" and fails
    Then nothing is ejected and "c" is recorded as a recompose
    And "c" is ejected once it fails with "a" ahead of it

  Scenario: A red tested on current main with exactly the changes ahead of it is ejected
    Given changes "a" and "c" are queued
    When "c" is tested with "a" ahead of it and fails
    Then "c" is ejected with no recompose

  Scenario: After a restart the result is read again and checked before any eject
    Given "c" is being tested without "a", which is queued ahead of it
    When the queue restarts and "c" fails
    Then the restarted queue reads the result of "c" again
    And nothing is ejected and "c" is recorded as a recompose

  Scenario: A recompose of several changes is one settle record
    Given changes "a" and "b" are being tested together on main
    When main moves outside the queue and their result comes in
    Then exactly one recompose record is kept, naming "a" and "b"
