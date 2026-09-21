@subscription @review
Feature: Explicit live subscription acceptance
  This private fixture requires owner-authorized credentials and a real model.
  It is deliberately outside the default feature glob and must be invoked explicitly.

  Scenario: A real subscription review finds a seeded regression and erases its session
    When the authorized subscription reviews the private first-byte regression
    Then the live review locates the regression with verified provenance and no credentials
