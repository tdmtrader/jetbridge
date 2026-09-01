Feature: The build factory exposes persisted build policy

  Source: all 25 specs in atc/db/build_factory_test.go. The scenarios group the
  status tables while using production factories and isolated PostgreSQL rows.

  Scenario: A build can be loaded by its persisted identity
    When the real build factory evaluates profile "lookup"
    Then the build factory result is "found=true;same=true"

  Scenario Outline: One-off interceptibility follows completion and grace policy
    When the real build factory evaluates profile "<profile>"
    Then the build factory result is "<result>"

    Examples:
      | profile              | result                                                        |
      | one-off-within-grace | succeeded=true;aborted=false;errored=false;failed=false        |
      | one-off-past-grace   | succeeded=false;aborted=false;errored=false;failed=false       |
      | one-off-running      | interceptible=true                                            |

  Scenario Outline: Pipeline build interceptibility follows lifecycle policy
    When the real build factory evaluates profile "<profile>"
    Then the build factory result is "<result>"

    Examples:
      | profile                   | result                                                        |
      | pipeline-completed        | succeeded=false;aborted=false;errored=false;failed=false       |
      | pipeline-running          | pending=true;started=true                                      |
      | pipeline-failed-immediate | false,false,false,false                                        |

  Scenario: Visibility, all-build, and public-build queries filter real rows
    When the real build factory evaluates profile "visibility"
    Then the build factory result is "visible=true;all=true;public=true;private-pipeline=true"

  Scenario: Drainable builds exclude checks, running builds, and drained builds
    When the real build factory evaluates profile "drainable"
    Then the build factory result is "count=1;match=true"

  Scenario: Started-build queries include started one-off and job builds
    When the real build factory evaluates profile "started"
    Then the build factory result is "count=2;match=true"

  Scenario: Date pagination includes only builds in the requested window
    When the real build factory evaluates profile "date-pages"
    Then the build factory result is "inside=2;future=0;old=0"
