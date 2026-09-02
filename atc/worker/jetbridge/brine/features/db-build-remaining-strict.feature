Feature: Remaining persisted build state follows production database semantics

  Source: production-only SavePipeline and Abort leaves remaining in
  atc/db/build_test.go. Every scenario uses production factories and a fresh
  real PostgreSQL database.

  Scenario Outline: Remaining DB build profile <profile> has its exact result
    Given the remaining real DB build evaluates profile "<profile>"
    Then the remaining DB build observation is "<expected>"

    Examples:
      | profile                     | expected                      |
      | save-pipeline/new-parent    | parent=true                   |
      | save-pipeline/latest-only   | parent=true;error=newer-build |
      | save-pipeline/update-parent | parent=true                   |
      | save-pipeline/unarchive     | paused=false                  |
      | save-pipeline/keep-paused   | paused=true                   |
      | abort/pending-schedule      | advanced=true                 |
      | abort/finished-schedule     | unchanged=true                |
      | finish-archive/direct       | archived=true                 |
      | finish-archive/descendants  | all-archived=true             |
