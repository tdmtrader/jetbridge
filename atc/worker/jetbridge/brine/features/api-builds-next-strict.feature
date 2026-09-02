Feature: Additional strict builds API behavior

  Source: 27 exact leaves in atc/api/builds_test.go. Every scenario uses a
  real TCP listener and http.Server, production routing, handlers, accessors
  and PostgreSQL-backed teams, pipelines, jobs and builds.

  Scenario Outline: The additional builds API behavior is exact for <profile>
    Given the next strict builds API executes profile "<profile>"
    Then the next strict builds API observation exactly matches profile "<profile>"

    Examples:
      | profile |
      | builds-next-list-public-header |
      | builds-next-list-auth-header |
      | builds-next-list-public-links |
      | builds-next-list-auth-links |
      | builds-next-invalid-get |
      | builds-next-invalid-resources |
      | builds-next-missing-get |
      | builds-next-missing-resources |
      | builds-next-missing-events |
      | builds-next-missing-preparation |
      | builds-next-missing-plan |
      | builds-next-missing-abort |
      | builds-next-get-public-status |
      | builds-next-get-outsider-status |
      | builds-next-get-owner-status |
      | builds-next-resources-public-status |
      | builds-next-resources-owner-status |
      | builds-next-resources-owner-header |
      | builds-next-preparation-public-status |
      | builds-next-preparation-owner-status |
      | builds-next-preparation-owner-header |
      | builds-next-plan-public-status |
      | builds-next-plan-owner-status |
      | builds-next-plan-owner-header |
      | builds-next-plan-owner-body |
      | builds-next-plan-public-no-plan-status |
      | builds-next-plan-owner-no-plan-status |
