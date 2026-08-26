@PE-01 @PE-02 @PE-04 @CO-04 @CO-05 @VT-02 @VT-03
Feature: Running a step, and what the caller gets back

  Two things decide what a step's Run does: whether the worker has an exec
  transport, and what the caller declared it needed. With no transport the pod
  IS the step. With one, the pod is a placeholder the step is exec'd into and
  which has to outlive it, because the step's outputs are still inside it.
  Either way the caller was handed a set of volumes before anything was
  scheduled, and those volumes are the only route back to the step's results.

  Source: k8s_runtime_behavioral_spec_20260331 (PE-01, PE-02, PE-04) and
  jetbridge_storage_behavioral_spec_20260330 (CO-04, CO-05, VT-02, VT-03).
  Migrated from container_test.go.

  # --------------------------------------------------------------------------
  # Direct mode — the pod is the step
  # --------------------------------------------------------------------------

  # PE-02. With no exec transport there is nowhere to exec FROM, so the command
  # has to be the pod's own entrypoint. Building a pause pod here would leave
  # the step sleeping for a day while nothing ever ran the command.
  #
  # Worth recording against coverage_matrix.md's PE-01 drift note, which reads
  # the ginkgo Describe title "Run uses exec-mode for all tasks (universal
  # pause pod)" as the implementation having moved past the spec. It has not:
  # container.go's first line of Run is `execMode := c.executor != nil`, and
  # this scenario is the branch that title says does not exist.
  @PE-02
  Scenario: Without an exec transport the pod runs the command itself
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "direct-cmd-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the step runs "echo hello" with no exec transport configured
    Then the pod itself carries the step's command
    And that pod works in "/tmp/build/workdir"
    And the step has an identity a restarted web could attach to

  # PE-04's pod-level clause, which the coverage matrix records as having no
  # named test. A build step that runs unconfined can issue syscalls the
  # runtime default blocks; on a shared cluster that is the difference between
  # a sandbox and a foothold.
  @PE-04
  Scenario: A step is confined even when nobody asked for confinement
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "seccomp-handle" built from image "docker:///busybox"
    When the step runs "echo hello" with no exec transport configured
    Then the step is confined by the runtime's default seccomp profile

  # A step that declares no workspace must not be given one. An unasked-for
  # emptyDir at a path the image already populates silently shadows it, and the
  # step fails looking for files that are right there in the image.
  @CO-04
  Scenario: A step that declares no working directory is given no workspace
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "no-dir-handle" built from image "docker:///busybox"
    And it declares no working directory
    When the container runs
    Then the pod has 0 volumes
    And the step has nothing mounted at all

  # DISPOSITION — "creates a Pod with an emptyDir volume for spec.Dir when Dir
  # is set" is the positive half of the scenario above and is already asserted
  # by container-pod.feature's "A step sees its working directory and every
  # input" (CO-04), which checks both the mount and its ephemerality. Not
  # duplicated here.
  #
  # DISPOSITION — the remaining clauses of "creates a Pod with the correct
  # image, command, args, and env" are covered elsewhere: the image by
  # container-spec.feature (PE-05), the environment by PE-06, the pod name by
  # pod-naming.feature, RestartPolicy by container-pod.feature (PE-03), and
  # AllowPrivilegeEscalation by PE-04 there. Only the command, the working
  # directory and the seccomp profile were unclaimed, and they are above.

  # --------------------------------------------------------------------------
  # Exec mode — the pod is a placeholder the step outlives
  # --------------------------------------------------------------------------

  # PE-01. The pod must not be the step, because a pod that is the step dies
  # with it, taking the step's outputs and any chance of intercepting a failure
  # with it. The ginkgo test pinned the exact pause string; what a consumer can
  # observe is that the pod's entrypoint is NOT the command, and that the
  # command ran anyway.
  @PE-01
  Scenario: With an exec transport the pod is a placeholder the step runs inside
    Given a jetbridge worker that really runs task commands
    When a task "placeholder-task" runs "echo hello"
    Then the build log contains "hello"
    And the pod is a placeholder, not the step's command
    And the pod is still on the cluster afterwards

  # A pod deleted the moment the command exits cannot have its outputs streamed
  # out and cannot be intercepted. Cleanup is the collector's job, and it waits
  # for the build.
  @PE-01
  Scenario: A failed step's pod is kept for the operator, not cleaned up on the spot
    Given a jetbridge worker that really runs task commands
    When a task "kept-after-failure" runs "exit 42"
    Then the task exits 42
    And the pod is still on the cluster afterwards

  # --------------------------------------------------------------------------
  # What FindOrCreateContainer hands back before anything is scheduled
  # --------------------------------------------------------------------------

  # CO-04/CO-05. These mounts are how the build step finds its own inputs,
  # outputs and caches; a missing one means the step's artifact is never
  # registered and the next step reads nothing. Two sharing a handle is worse —
  # the registry then points two paths at one blob.
  @CO-04 @CO-05
  Scenario: Every path the step declared comes back as its own volume
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "vm-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/my-input"
    And it takes an input at "/tmp/build/workdir/other-input"
    And it produces an output at "/tmp/build/workdir/result"
    And it caches "/tmp/build/workdir/.cache"
    When the container is created but not yet run
    Then the caller is handed 5 volumes in all
    And the caller is handed a volume mounted at "/tmp/build/workdir"
    And the caller is handed a volume mounted at "/tmp/build/workdir/my-input"
    And the caller is handed a volume mounted at "/tmp/build/workdir/other-input"
    And the caller is handed a volume mounted at "/tmp/build/workdir/result"
    And the caller is handed a volume mounted at "/tmp/build/workdir/.cache"
    And every volume the caller was handed has its own handle

  # The volumes exist before the pod does, because the command is not known
  # until Run. Until then they cannot name a pod, and a volume that claimed one
  # early would stream out of whatever pod last held that name.
  @CO-04
  Scenario: A volume handed back before the step runs knows no pod
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "deferred-pod-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/my-input"
    When the container is created but not yet run
    Then no volume the caller was handed knows a pod yet

  @CO-04
  Scenario: Running the step is what binds its volumes to a pod
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "deferred-pod-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/my-input"
    When the container is created but not yet run
    And the step then runs
    Then every volume the caller was handed now names the pod "deferred-pod-handle"

  # DISPOSITION — the four "returns Volumes with an executor wired up for
  # StreamIn/StreamOut" cases assert `vol.HasExecutor()`, which is wiring, not
  # behavior. What a consumer experiences is covered from both ends already:
  # volume-streaming.feature proves a wired volume moves bytes, and its
  # "a stub volume with no cluster behind it" scenarios prove an unwired one
  # refuses rather than silently returning nothing.

  # --------------------------------------------------------------------------
  # Getting the step's output back out
  # --------------------------------------------------------------------------

  # VT-02/VT-03, and coverage_matrix.md Addendum 2's round trip applied to the
  # "Output volume extraction after exec" block. That block asserted
  # `lastCall.command == ["tar","cf","-","-C",path,"."]` and
  # `lastCall.podName` — neither of which proves a single byte moved, and both
  # of which would still pass if the archive came back empty. Here the step
  # really writes a file and the file really comes back.
  @VT-03
  Scenario: A step's output can be read back out of the volume afterwards
    Given a step "outputsurvives" that writes "hello-from-the-step" into its output directory
    When its output is streamed out of the volume the caller was handed
    Then the streamed output holds "output.txt" containing "hello-from-the-step"

  # DISPOSITION — "pod remains running after exec for output extraction" is the
  # same assertion as "A failed step's pod is kept for the operator" above, in
  # the success direction, and the round trip immediately above only works
  # because the pod is still there. Not duplicated a third time.

  # DISPOSITION — "Input streaming is a no-op (handled by init containers)"
  # has no seam-level equivalent and is not migrated. It asserts
  # `len(execExecutor.execCalls) == 1`, i.e. that the runtime did NOT do
  # something; the only observation is the double's call count, which is
  # Addendum 2's "routing" class. Worse, the block's own title is not what it
  # tests: this container is built with no storage backend, so
  # buildArtifactInitContainers returns nil and there are no init containers in
  # the pod at all. The init-container staging the title names is asserted by
  # behavioral_permutations_test.go's TestBuildArtifactInitContainers_* family
  # and by daemonset_integration_test.go, both of which configure a backend.

  # --------------------------------------------------------------------------
  # fly hijack
  # --------------------------------------------------------------------------

  # The exit code of an intercepted command is what lands in the operator's own
  # shell. Swallowing it makes a failed hijack look clean, which is how a
  # broken debugging session gets mistaken for a working one.
  Scenario: An intercepted command's exit code reaches the operator
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "exit 130"
    Then the intercepted command exits 130

  # DISPOSITION — "execs into the existing pod without creating a new one" is
  # already worker.feature's "Intercepting a step attaches to the pod the step
  # created", which asserts the command's output reaches the operator AND that
  # the cluster still holds only the one pod. The ginkgo case additionally
  # asserted `execCalls[0].podName`; that is the routing class, and the
  # single-pod assertion covers its effect.

  # DISPOSITION — "passes TTY flag through to executor" and "does not set TTY
  # when ProcessSpec.TTY is nil" are not migrated. They are the PE-08 case
  # named in coverage_matrix.md Addendum: the requirement is that a TTY
  # combines stdout and stderr into one stream, and that combining is
  # Kubernetes' behavior, not jetbridge's. Any double that exhibited it would
  # be simulating the thing under test. Jetbridge's actual responsibility is to
  # pass the flag, which is an integration-boundary contract — live suite,
  # demote, or delete, but not a behavioral scenario.

  # --------------------------------------------------------------------------
  # What the operator's counters say a Run did
  # --------------------------------------------------------------------------

  # These two counters are what a container-churn dashboard is built on. A
  # failure counted as a success hides a cluster that has stopped admitting
  # pods behind a healthy-looking creation rate.
  Scenario: A container the runtime created is counted
    Given a jetbridge worker on a fake Kubernetes cluster
    When a step is run on it
    Then the operator sees 1 container created and 0 failed

  Scenario: A placeholder pod counts the same as a step's own pod
    Given a jetbridge worker on a fake Kubernetes cluster
    When a step is run on it through an exec transport
    Then the operator sees 1 container created and 0 failed

  Scenario: A pod the cluster refused is counted as a failure, not a creation
    Given a jetbridge worker on a fake Kubernetes cluster
    When a step is run on it but the cluster refuses to create the pod
    Then the operator sees 0 container created and 1 failed

  # --------------------------------------------------------------------------
  # When the database, not Kubernetes, is what went wrong
  # --------------------------------------------------------------------------

  # The lookup is the first thing the request does. A lost connection has to
  # surface as a lookup failure; treating it as "not found" would insert a
  # second row for a container that already exists and orphan the first one's
  # pod.
  Scenario: A lost database connection is reported as a lookup failure
    Given a worker that lost its database connection before the container was requested
    Then requesting the container fails saying "find container in db"

  # Handles are globally unique. One already taken on another worker misses
  # this worker's lookup and then collides on insert. Reporting anything other
  # than a create failure would schedule the step against a row it does not
  # own.
  Scenario: A handle another worker already holds cannot be claimed
    Given a container "dup-handle" whose handle another worker already holds
    Then requesting the container fails saying "create container in db"

  # A row left in `creating` by a web that died mid-request is invisible to the
  # collector and never reclaimed. The next request has to adopt it: a second
  # row would orphan the first one's pod, and leaving it alone leaks it
  # forever.
  Scenario: A container a crash left half-created is adopted, not duplicated
    Given a container "stale-creating-handle" left half-created by a crash, requested again
    Then requesting the container succeeds
    And the container row "stale-creating-handle" is left in state "created"
    And the database holds exactly 1 row for the handle "stale-creating-handle"

  # And when adopting it fails too, it must be marked failed rather than left
  # in `creating` — `failed` is the state the collector can actually see.
  Scenario: A half-created container that still cannot be completed is left for the collector
    Given a container "stale-fail-handle" left half-created by a crash on a database that still cannot complete it
    Then requesting the container fails saying "mark container as created"
    And the container row "stale-fail-handle" is left in state "failed"

  # DISPOSITION — "marks the container as failed when Created() fails" (the
  # non-stale path) is already worker.feature's "A container that cannot be
  # recorded is left for the collector", same fault, same two assertions.

  # PE-08. `fly hijack` gives an interactive shell, and it only behaves like
  # one if the exec actually allocates a terminal. The command is its own
  # witness here: `test -t 1` is the check every interactive tool makes.
  #
  # behavioral_runtime_spec_test.go covered this by recording the ExecInPod
  # call and asserting calls[0].tty — a spy on a parameter every double in
  # this package declares as `_`. Nothing observed what the flag DOES, so the
  # executor could ignore it entirely and the assertion would still pass.
  @PE-08
  Scenario: A step that asks for a terminal gets a real one
    When a step asks whether it has a terminal, with one attached
    Then the step reports "terminal"

  # The other direction, and the reason it matters: a step given a terminal it
  # did not ask for has its stderr folded into stdout, which for a resource
  # step corrupts the JSON the runtime parses back.
  @PE-08
  Scenario: A step that asks for no terminal does not get one
    When a step asks whether it has a terminal, with none attached
    Then the step reports "pipe"
