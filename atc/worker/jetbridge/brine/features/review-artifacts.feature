@review
Feature: Capturing a committed change for an inspection review

  Humans and agents consume the same local command and report contract.
  These scenarios run the compiled commands against real Git repositories and
  communicate with the worker's private input reader over real stdio pipes.

  Background:
    Given a committed review change with a deleted file and an external plan

  Scenario: Capture binds exact commits and keeps context omitted by git archive
    When the review change is captured from the command line
    Then the captured trees and plan match the committed inputs
    And recapturing the same change produces the same input digest

  Scenario Outline: Unsupported repository states fail before publishing input
    Given the review repository contains <state>
    When the review change is captured from the command line
    Then capture fails naming <reason> without publishing a bundle

    Examples:
      | state       | reason        |
      | "dirty"     | "uncommitted" |
      | "untracked" | "uncommitted" |
      | "submodule" | "submodules"  |
      | "lfs"       | "Git LFS"     |

  Scenario: A fresh input reader can inspect the capture after the CLI exits
    When the review change is captured from the command line
    And a fresh review input reader reads "head/parser.go"
    Then the review reader returns text containing "s[1]"

  Scenario: The reader cannot open a credential file beside the capture
    When the review change is captured from the command line
    And a fresh review input reader reads "../auth.json"
    Then the review reader refuses the path

  Scenario: Modified captured bytes cannot be served as the original input
    When the review change is captured from the command line
    And the captured head file is modified
    And a fresh review input reader reads "head/parser.go"
    Then the review reader refuses to start
