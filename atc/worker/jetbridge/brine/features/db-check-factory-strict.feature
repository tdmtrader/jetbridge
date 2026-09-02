Feature: Check factory persistence and enumeration

  Scenario Outline: Check factory profile <profile> has observation <observation>
    When the production check factory evaluates profile "<profile>"
    Then the check factory observation is "<observation>"

    Examples:
      | profile                  | observation |
      | resource-return          | error=false;created=true;id-positive=true;name=check;resource-match=true |
      | resource-plan            | count=1;manual=false;plan-id=true;name=some-name;resource=some-name;type=some-base-resource-type;tags=tag-a,tag-b;from=from=version;interval=1m0s;skip=false;source=some=source;base=some-base-resource-type |
      | interval-skip            | error=false;created=false;nil=true;count=0 |
      | manual-trigger           | error=false;created=true;nil=false;manual=true;skip=true |
      | running-build            | error=false;created=false;nil=true;count=1 |
      | webhook-plan             | from=from=version;interval=1h0m0s;source=some=source;base=some-base-resource-type |
      | webhook-skip             | error=false;created=false;nil=true;count=0 |
      | explicit-interval-plan   | from=from=version;interval=42s;source=some=source;base=some-base-resource-type |
      | never-plan               | from=from=version;never=true;source=some=source;base=some-base-resource-type |
      | parent-plan              | from=from=version;interval=1m0s;source=sdk=sdk,some=source;base=some-base-type;get=true;get-name=custom-type;get-type=some-base-type;get-source=some=type-source;get-tags=some-tag;check=true;check-type=custom-type |
      | parent-return            | error=false;created=true;id-positive=true;resource-match=true |
      | parent-start             | manual=false;plan-id=true;resource=some-name |
      | resource-type-plan       | name=some-type;resource-type=some-type;from=from=version;interval=1h0m0s;source=some=type-source;base=some-base-type |
      | resource-type-return     | error=false;created=true;id-positive=true;name=check |
      | resource-type-start      | count=1;manual=false;plan-id=true |
      | resources-used           | count=1;names=some-resource;error=false |
      | resources-inactive       | count=0;error=false |
      | resources-paused         | count=0;error=false |
      | resources-put-failed     | count=2;error=false |
      | resources-put-succeeded  | count=1;error=false |
      | resource-types-list      | pipelines=2;first=some-type;second=some-other-type,some-type;error=false |
      | resource-types-inactive  | pipelines=0;error=false |
      | resource-types-paused    | pipelines=0;error=false |
