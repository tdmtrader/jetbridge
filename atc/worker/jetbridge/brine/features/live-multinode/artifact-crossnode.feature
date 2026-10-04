@live-kubernetes @artifact-handoff @VT-08 @cross-node
Feature: An artifact crosses nodes through the node daemons

  The live artifact fixture runs on every approved node at once -- a store and
  a daemon on each, one root path -- so a task's output can be produced on one
  node and read on another, the way a cluster's artifact daemons serve it.
  This tier needs two approved nodes up and runs only when there are.

  Scenario: A task's output on one node reaches a following task on another
    Given a task using Kubernetes
    When a task on one approved node produces "nested/result.txt" containing "built on one node" for a following task on another
    Then the following task received exactly that artifact from the other node
