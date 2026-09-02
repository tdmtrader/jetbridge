Feature: Remaining builds API authorization boundaries

  Source: 20 exact authorization leaves in atc/api/builds_test.go. Each
  scenario crosses a real TCP listener and http.Server, production routes,
  access wrappers and PostgreSQL-backed teams, pipelines, jobs and builds.

  Scenario Outline: The builds API enforces its exact authorization boundary
    Given the strict remaining builds API executes profile "<profile>"
    Then the strict remaining builds API observation exactly matches profile "<profile>"

    Examples:
      | profile |
      | builds-post-unauthorized |
      | builds-post-forbidden |
      | builds-get-one-off-unauthorized |
      | builds-get-private-unauthorized |
      | builds-resources-one-off-unauthorized |
      | builds-resources-private-unauthorized |
      | builds-resources-forbidden |
      | builds-events-forbidden |
      | builds-events-private-unauthorized |
      | builds-events-public-job-private-unauthorized |
      | builds-abort-unauthorized |
      | builds-abort-forbidden |
      | builds-preparation-forbidden |
      | builds-preparation-one-off-unauthorized |
      | builds-preparation-private-unauthorized |
      | builds-preparation-public-job-private-unauthorized |
      | builds-plan-forbidden |
      | builds-plan-one-off-unauthorized |
      | builds-plan-private-unauthorized |
      | builds-plan-public-job-private-unauthorized |
