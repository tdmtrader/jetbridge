Feature: The queue makes progress on its own
  One loop takes admitted changes all the way to main: it tests them, records
  each result, lands or ejects them, and announces every decision. It keeps
  its place across a restart, and an announcement that fails never changes
  what was decided.

  Scenario: A green batch lands and is announced
    Given changes "a", "b" and "c" are admitted
    When the queue runs and their batch passes
    Then main moves to their candidate once
    And "a", "b" and "c" are announced as landed

  Scenario: A red change is ejected with its reason and the changes built on it follow
    Given changes "a", "b" and "c" are admitted
    And "c" builds on "b"
    And only "b" is broken
    When the queue runs
    Then "a" lands
    And "b" is announced as ejected because it failed on its own
    And "c" is announced as ejected, naming "b" as the cause

  Scenario: A runner timeout is retried, then the queue pauses and ejects nothing
    Given one retry is allowed when there is no verdict
    And changes "a" and "b" are admitted
    When every run times out
    Then the batch is run twice
    And the queue is announced as paused, with the reason kept
    And "a" and "b" are still queued and nothing is ejected

  Scenario: A flaky batch lands and the flake is announced
    Given changes "a" and "b" are admitted
    When their batch fails but each half passes on its own
    Then "a" and "b" land
    And the batch of "a" and "b" is announced as flaky

  Scenario: A failing notifier never blocks a landing
    Given every announcement fails
    And change "a" is admitted
    When the queue runs and its batch passes
    Then "a" lands
    And the failed announcement is logged

  Scenario: A restart mid-run loses nothing and lands nothing twice
    Given changes "a" and "b" are admitted
    When the queue stops while starting their run
    And a new queue picks up the saved state and stops again while landing
    And a new queue picks up and stops right after main moves
    And another new queue picks up and runs
    Then "a" and "b" each land exactly once
    And nothing is left queued or in flight

  Scenario: A crash right after landing never ejects what landed
    Given change "a" is queued
    When the queue lands "a" and stops before it records the landing
    And a new queue picks up the saved state
    And a rerun of "a" would fail
    Then "a" is recorded as landed
    And nothing is ejected

  Scenario: A paused queue starts nothing until it is resumed
    Given the queue is paused with change "a" queued
    When the queue runs
    Then no run starts
    When the queue is resumed
    Then the resume is announced
    And "a" lands

  Scenario: A second driver cannot act on a queue another driver holds
    Given one driver holds the queue and stopped right after main moved
    When a second driver admits, runs or resumes the queue
    Then each is refused because the queue is held
    And the saved state is untouched and nothing is reconciled

  Scenario: A stalled driver whose lease was taken over cannot land
    Given a driver stalls past its lease while landing "a"
    When a second driver takes over the queue and admits "b"
    Then the stalled landing is refused
    And the second driver lands "a" and "b"

  Scenario: A landing refused because main moved is recomposed and never counts toward the alarm
    Given the lander refuses a green batch five times because main moved
    When the queue runs five times
    Then each is recorded as a recompose with the reason and the batch stays queued
    And the queue is not paused and nothing is ejected
    And the batch lands once main stops moving

  Scenario: A recompose neither counts nor clears the count of other landing failures
    Given two landings failed for another reason and one was refused because main moved
    When a third other landing fails
    Then the count of failed landings is three and the queue is not paused

  Scenario: Landing failures under max_failures are counted across drivers, with the last error
    Given every landing fails and change "a" is admitted
    When the queue is rebuilt and run until main has been tried twice
    Then the saved state counts two failed landings and the last error, and the queue is not paused

  Scenario: Repeated landing failures never pause or eject, and the queue keeps retrying the land
    Given every landing fails
    And change "a" is admitted
    When the queue runs until main has been tried six times
    Then the queue is not paused, no pause is announced and nothing is ejected
    And "a" is still queued and the count of failed landings keeps rising

  Scenario: A successful land clears the count of landing failures
    Given three landings fail and then main accepts the land
    When the queue runs on
    Then "a" lands and the saved count of failed landings and the last error are cleared

  Scenario: A landing that is still in flight can never land after the queue has moved on
    Given change "a" is admitted and passes
    When its landing outlives the queue's wait and the queue moves on to rerun "a"
    Then the late landing is refused
    And "a" is never both on main and ejected
