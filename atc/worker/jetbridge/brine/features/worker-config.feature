@CF-01 @CF-02 @CF-03 @CF-07 @WR-05 @WR-06
Feature: Configuring the Kubernetes worker

  What an operator gets when they configure the worker, and what they get when
  they configure nothing: the namespace it runs pods in, how long it waits for
  one to start, where caches live, and which image each resource type resolves
  to.

  Source: k8s_runtime_behavioral_spec_20260331 — CF-01, CF-02, CF-03, CF-07;
  and WR-05, WR-06 for the resource-type image mapping. Migrated whole from
  config_test.go — verified case by case: all 17, which carried no requirement identifiers.

  # CF-01
  Scenario Outline: The namespace falls back to "default" — <case>
    Given a worker configured for namespace "<given>"
    Then the namespace is "<resolved>"

    Examples:
      | case          | given        | resolved     |
      | named         | my-namespace | my-namespace |
      | left empty    |              | default      |

  # CF-02
  Scenario: A pod gets five minutes to start unless told otherwise
    Given a worker configured for namespace "my-namespace"
    Then the pod startup timeout is 5 minutes

  # CF-07
  Scenario: Caches live under a fixed path
    Given a worker configured for namespace "my-namespace"
    Then caches are stored under "/concourse/cache"

  # CF-03. The three ways a clientset can be asked for, and what each yields.
  Scenario: A kubeconfig file on disk produces a clientset
    Given a worker configured with a kubeconfig file that exists
    When a clientset is built from it
    Then a working clientset comes back

  Scenario: A kubeconfig path that does not exist is an error, not a panic
    Given a worker configured for namespace "my-namespace" with kubeconfig "/nonexistent/kubeconfig"
    When a clientset is built from it
    Then it fails to build a clientset

  Scenario: With no kubeconfig and no cluster, there is nothing to connect to
    Given a worker configured for namespace "my-namespace"
    When a clientset is built from it
    Then it fails to build a clientset

  # WR-05: the built-in resource types an operator gets for free.
  Scenario: Resource types resolve to their built-in images by default
    Given the resource type images are merged with no overrides
    Then the resource type "git" resolves to image "concourse/git-resource"
    And the resource type "registry-image" resolves to image "concourse/registry-image-resource"
    And the resource type "time" resolves to image "concourse/time-resource"

  # WR-06: overrides. Every row also asserts the untouched defaults survive,
  # because an override that silently drops the rest of the map would break
  # every pipeline using a type the operator did not mention.
  Scenario Outline: An operator override replaces one image — <case>
    Given the resource type images are merged with the overrides "<overrides>"
    Then the resource type "<type>" resolves to image "<image>"
    And the resource type "registry-image" resolves to image "concourse/registry-image-resource"

    Examples:
      | case             | overrides                                   | type        | image                                |
      | replace a default| git=my-registry/custom-git-resource         | git         | my-registry/custom-git-resource      |
      | add a new type   | custom-type=my-registry/custom-resource     | custom-type | my-registry/custom-resource          |
      | image with a tag | git=my-registry/git-resource:v2.0           | git         | my-registry/git-resource:v2.0        |
      | image by digest  | git=my-registry/git-resource@sha256:abc123  | git         | my-registry/git-resource@sha256:abc123 |
      | last one wins    | git=first-image,git=second-image            | git         | second-image                         |

  Scenario: Several overrides merge together and leave the rest alone
    Given the resource type images are merged with the overrides "git=my-registry/git,s3=my-registry/s3,new-type=my-registry/new"
    Then the resource type "git" resolves to image "my-registry/git"
    And the resource type "s3" resolves to image "my-registry/s3"
    And the resource type "new-type" resolves to image "my-registry/new"
    And the resource type "time" resolves to image "concourse/time-resource"

  Scenario: A malformed override is skipped rather than accepted
    Given the resource type images are merged with the overrides "malformed-no-equals,git=valid-image"
    Then the resource type "git" resolves to image "valid-image"
    And there is no resource type "malformed-no-equals"

  # The defaults are a package-level map. Mutating it would leak one worker's
  # overrides into the next one constructed in the same process.
  Scenario: Merging does not mutate the shared defaults
    Given the resource type images are merged with the overrides "git=my-registry/custom"
    Then the resource type "git" resolves to image "my-registry/custom"
    And the built-in defaults were left untouched
