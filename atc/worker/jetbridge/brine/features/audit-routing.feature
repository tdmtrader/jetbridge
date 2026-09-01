Feature: Audit categories emit only the configured user-facing records

  Source: all 37 specs in atc/auditor/auditor_test.go. Every row invokes the
  production Auditor with a real HTTP request and observes its structured log
  output. The in-memory lager sink is the audit output under test, not a fake
  collaborator; there are no handler, database, or accessor doubles.

  Scenario: Enabling every audit category handles every production route
    When the production auditor evaluates category "all" with case "all-routes"
    Then the audit routing result is "logged=true;action=all-routes"

  Scenario Outline: The <category> audit switch in case <case> produces <result>
    When the production auditor evaluates category "<category>" with case "<case>"
    Then the audit routing result is "<result>"

    Examples:
      | category  | case           | result                                      |
      | build     | disabled-match | logged=false;action=GetBuildPlan             |
      | build     | enabled-match  | logged=true;action=GetBuildPlan              |
      | build     | enabled-other  | logged=false;action=SaveConfig               |
      | build     | disabled-other | logged=false;action=SaveConfig               |
      | container | disabled-match | logged=false;action=GetContainer             |
      | container | enabled-match  | logged=true;action=GetContainer              |
      | container | enabled-other  | logged=false;action=SaveConfig               |
      | container | disabled-other | logged=false;action=SaveConfig               |
      | job       | disabled-match | logged=false;action=GetJob                   |
      | job       | enabled-match  | logged=true;action=GetJob                    |
      | job       | enabled-other  | logged=false;action=SaveConfig               |
      | job       | disabled-other | logged=false;action=SaveConfig               |
      | pipeline  | disabled-match | logged=false;action=GetPipeline              |
      | pipeline  | enabled-match  | logged=true;action=GetPipeline               |
      | pipeline  | enabled-other  | logged=false;action=SaveConfig               |
      | pipeline  | disabled-other | logged=false;action=SaveConfig               |
      | resource  | disabled-match | logged=false;action=GetResource              |
      | resource  | enabled-match  | logged=true;action=GetResource               |
      | resource  | enabled-other  | logged=false;action=SaveConfig               |
      | resource  | disabled-other | logged=false;action=SaveConfig               |
      | system    | disabled-match | logged=false;action=SaveConfig               |
      | system    | enabled-match  | logged=true;action=SaveConfig                |
      | system    | enabled-other  | logged=false;action=GetBuild                 |
      | system    | disabled-other | logged=false;action=GetBuild                 |
      | team      | disabled-match | logged=false;action=ListTeams                |
      | team      | enabled-match  | logged=true;action=ListTeams                 |
      | team      | enabled-other  | logged=false;action=SaveConfig               |
      | team      | disabled-other | logged=false;action=SaveConfig               |
      | worker    | disabled-match | logged=false;action=ListWorkers              |
      | worker    | enabled-match  | logged=true;action=ListWorkers               |
      | worker    | enabled-other  | logged=false;action=SaveConfig               |
      | worker    | disabled-other | logged=false;action=SaveConfig               |
      | volume    | disabled-match | logged=false;action=ListVolumes              |
      | volume    | enabled-match  | logged=true;action=ListVolumes               |
      | volume    | enabled-other  | logged=false;action=SaveConfig               |
      | volume    | disabled-other | logged=false;action=SaveConfig               |
