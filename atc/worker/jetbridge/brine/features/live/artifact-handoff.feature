@live-kubernetes @artifact-handoff @VT-08
Feature: Artifact handoff through real tasks and the node daemon

  Scenario Outline: Artifact handoff delivers exact files or reports the transfer failure
    Given a task using Kubernetes
    When a task produces "<file>" containing "<content>" and hands it to a following task using "<encoding>" with fault "<fault>"
    Then the handoff reports "<outcome>"

    Examples:
      | file              | content           | encoding | fault            | outcome                                               |
      | result.json       | built ok          | raw      | none             | exact artifact delivered                              |
      | nested/result.txt | nested build data | gzip     | none             | exact artifact delivered                              |
      | deep/tree/out.txt | packed build data | s2       | none             | exact artifact delivered                              |
      | result.json       | built ok          | raw      | write-refused    | stream into returned input volume: stream in via exec: |
      | result.json       | built ok          | raw      | producer-offline | artifact unavailable on its producer node or any peer           |
