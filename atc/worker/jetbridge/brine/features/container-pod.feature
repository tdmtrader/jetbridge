@PE-03 @PE-04 @PE-07 @CO-04 @CO-05 @CO-06 @CO-07 @CO-08 @CF-04 @CF-05 @SC-01 @SC-02 @SC-03 @SC-04
Feature: What a step's pod actually looks like

  A container spec is a description of what a build step needs. This is what
  Kubernetes is asked for on its behalf: the directories the step can write to,
  which of them survive the pod, how much of the node it may use, what it is
  allowed to do, and what runs alongside it.

  Source: k8s_runtime_behavioral_spec_20260331 (PE-03, PE-04, PE-07, CF-04,
  CF-05) and jetbridge_storage_behavioral_spec_20260330 (CO-04 to CO-08).
  Migrated from container_test.go.

  # CO-04. A step gets its working directory plus one volume per input.
  @CO-04
  Scenario: A step sees its working directory and every input
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "input-vol-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/input-a"
    And it takes an input at "/tmp/build/workdir/input-b"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/build/workdir"
    And the step sees a volume mounted at "/tmp/build/workdir/input-a"
    And the step sees a volume mounted at "/tmp/build/workdir/input-b"
    And every volume is ephemeral

  # CO-05. An output written into an input's directory must not get a second
  # volume, or the step would write into one and the next step would read the
  # other.
  @CO-05
  Scenario: An output that shares an input's path gets one volume, not two
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "overlap-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/shared"
    And it produces an output at "/tmp/build/workdir/shared"
    When the container runs
    Then the pod has 2 volumes
    And the step sees a volume mounted at "/tmp/build/workdir/shared"

  @CO-05
  Scenario: An output on its own path gets its own volume
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "distinct-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/in"
    And it produces an output at "/tmp/build/workdir/out"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/build/workdir/in"
    And the step sees a volume mounted at "/tmp/build/workdir/out"

  # CO-08. Scratch space is ephemeral by design — persisting it across pods
  # would leak one build's temporary state into the next.
  @CO-08
  Scenario: Scratch space never outlives the pod
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "scratch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it uses scratch space at "/tmp/scratch"
    When the container runs
    Then the step sees a volume mounted at "/tmp/scratch"
    And the volume mounted at "/tmp/scratch" is lost with the pod

  @CO-07
  Scenario: A cache and a scratch path are different volumes
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "cache-scratch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it caches "/tmp/cache"
    And it uses scratch space at "/tmp/scratch"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/cache"
    And the step sees a volume mounted at "/tmp/scratch"

  # PE-07. The QoS class is what the kubelet uses to decide which pods to evict
  # first when a node runs out of memory, so it is the observable consequence
  # of the resource envelope — not an implementation detail.
  @PE-07
  Scenario: Limits alone reserve exactly what they cap
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "limits-handle" built from image "docker:///busybox"
    And it is limited to 1024 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step may use at most "1024m" CPU and "1Gi" memory
    And the step is reserved "1024m" CPU and "1Gi" memory
    And the pod is scheduled as "Guaranteed"

  @PE-07
  Scenario: Independent requests below the limits make the pod burstable
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "burstable-handle" built from image "docker:///busybox"
    And it is limited to 2048 CPU shares and 4294967296 bytes of memory
    And it requests 512 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step may use at most "2048m" CPU and "4Gi" memory
    And the step is reserved "512m" CPU and "1Gi" memory
    And the pod is scheduled as "Burstable"

  @PE-07
  Scenario: A step that asks for nothing is evicted first
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nolimits-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod is scheduled as "BestEffort"

  # PE-04. Privilege is the difference between a task that can mount things and
  # one that cannot; getting it backwards is a security hole in one direction
  # and a broken pipeline in the other.
  @PE-04
  Scenario: An unprivileged step cannot gain privileges
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "unpriv-handle" built from image "docker:///busybox"
    When the container runs
    Then the step cannot escalate its privileges

  @PE-04
  Scenario: A privileged step is granted privilege
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "priv-handle" built from image "docker:///busybox"
    And it runs privileged
    When the container runs
    Then the step can escalate its privileges

  # PE-03
  @PE-03
  Scenario: A step's pod is never restarted behind the scheduler's back
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "restart-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod is never restarted

  # SC-01 to SC-04. A sidecar exists to serve the step — a database for an
  # integration test, a log shipper. It must see what the step sees, and it must
  # not inherit the step's privilege.
  @SC-01
  Scenario: A step with no sidecars runs alone
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nosidecar-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod runs 1 containers

  @SC-01 @SC-02 @SC-04
  Scenario: A sidecar runs alongside the step and shares its working set
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/input-a"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the container runs
    Then the pod runs 2 containers
    And the sidecar "postgres" runs image "postgres:15"
    And the sidecar "postgres" sees the same volumes as the step
    And the sidecar "postgres" cannot escalate its privileges

  @SC-01
  Scenario: Several sidecars all run
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "multisidecar-handle" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    And a sidecar "redis" runs "redis:7" alongside it
    When the container runs
    Then the pod runs 3 containers
    And the sidecar "postgres" runs image "postgres:15"
    And the sidecar "redis" runs image "redis:7"

  # SC-03: a sidecar inherits the step's working directory unless it names one.
  @SC-03
  Scenario: A sidecar inherits the step's working directory
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-inherit-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And a sidecar "helper" runs "busybox" alongside it
    When the container runs
    Then the sidecar "helper" works in "/tmp/build/workdir"

  @SC-03
  Scenario: A sidecar that names a working directory keeps its own
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-own-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And a sidecar "helper" runs "busybox" alongside it
    And the sidecar "helper" declares its working directory as "/opt/helper"
    When the container runs
    Then the sidecar "helper" works in "/opt/helper"

  # CF-05. Registry credentials are an operator setting that has to reach every
  # pod, or images fail to pull with an error that looks like a pipeline bug.
  @CF-05
  Scenario: Operator-configured pull secrets and service account reach the pod
    Given a jetbridge worker that pulls with the secrets "registry-creds,gcr-key" as the service account "ci-runner"
    And a task container "secrets-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod pulls images using the secret "registry-creds"
    And the pod pulls images using the secret "gcr-key"
    And the pod runs as the service account "ci-runner"

  @CF-05
  Scenario: With nothing configured the pod names no credentials
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nosecrets-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod names no image pull secret and no service account

  @CF-05
  Scenario: A private registry's secret is added to the pod
    Given a jetbridge worker pulling from a private registry with secret "gcr-auth", already pulling with "existing-secret"
    And a task container "registry-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod pulls images using the secret "gcr-auth"
    And the pod pulls images using the secret "existing-secret"

  # Adding it twice would be harmless to Kubernetes but is a sign the merge is
  # wrong, and the original suite guarded it explicitly.
  @CF-05
  Scenario: A registry secret the operator already listed is not duplicated
    Given a jetbridge worker pulling from a private registry with secret "gcr-auth", already pulling with "gcr-auth"
    And a task container "registry-dup-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod names the secret "gcr-auth" exactly once

  # PE-07, the clause the QoS scenarios do not reach. Requests without limits
  # reserve capacity without capping it — a step that may burst but must be
  # guaranteed a floor.
  @PE-07
  Scenario: Requests without limits reserve a floor but set no ceiling
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "requests-only-handle" built from image "docker:///busybox"
    And it requests 512 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step is reserved "512m" CPU and "1Gi" memory
    And the pod is scheduled as "Burstable"

  # A step that writes a large artifact to local disk is evicted without an
  # ephemeral-storage reservation, and that eviction reads as an unexplained
  # failure rather than a capacity problem.
  @PE-07
  Scenario: Local disk is reserved and capped like any other resource
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "ephemeral-handle" built from image "docker:///busybox"
    And it is limited to 2147483648 bytes of local disk, requesting 1073741824
    When the container runs
    Then the step may use at most "2Gi" of local disk, reserving "1Gi"

  # SC-07. A sidecar exists to serve the step — a database, a log shipper. If
  # its output never reaches the build, a user debugging a failing integration
  # test has no way to see why the database rejected the connection.
  @SC-07
  Scenario: A sidecar's output reaches its own stream when there is one
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sc07-dedicated" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the step runs with a dedicated log stream for sidecar "postgres"
    Then the sidecar's output arrives on its own stream

  # And when there is no separate pane for it, the output still has to appear —
  # labelled, so it is distinguishable from the step's own.
  @SC-07
  Scenario: Without a separate stream the sidecar's output is labelled in the build log
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sc07-fallback" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the step runs with nowhere separate to put sidecar output
    Then the sidecar's output is folded into the build log, labelled "[postgres]"
