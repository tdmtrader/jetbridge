Feature: Remaining jobs API authorization boundaries

  Source: 13 exact authorization leaves in atc/api/jobs_test.go. Every
  scenario crosses a real TCP listener and http.Server, production routing and
  access wrappers, and PostgreSQL-backed teams, pipelines, jobs and builds.

  Scenario Outline: The jobs API enforces its exact authorization boundary
    Given the strict remaining jobs API executes profile "<profile>"
    Then the strict remaining jobs API observation exactly matches profile "<profile>"

    Examples:
      | profile |
      | jobs-get-unauthorized |
      | jobs-get-forbidden |
      | jobs-badge-forbidden |
      | jobs-list-unauthorized |
      | jobs-builds-forbidden |
      | jobs-inputs-unauthorized |
      | jobs-inputs-forbidden |
      | jobs-build-unauthorized |
      | jobs-build-forbidden |
      | jobs-pause-unauthorized |
      | jobs-unpause-unauthorized |
      | jobs-cache-unauthorized |
      | jobs-schedule-unauthorized |
