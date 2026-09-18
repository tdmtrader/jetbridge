@VT-02 @VT-03 @VT-04 @VT-05
Feature: Moving artifacts through volumes

  A consumer puts an artifact in, takes it back out, and hands it to the next
  step. Exact paths, contents and the execution destination determine whether
  that consumer receives the right artifact.

  Source: jetbridge_storage_behavioral_spec_20260330 — VT-02 (StreamIn),
  VT-03 (StreamOut), VT-04 (path resolution), VT-05 (stub volumes).

  Direct-volume round trips live in live/volume-io.feature and exercise real
  pod exec for the legacy direct-volume API. The returned-volume
  handoff outline in live/artifact-handoff.feature exercises production's
  daemon-backed read, input-init and real task execution paths.
  Focused Go tests remain for constructor arguments, execution attributes and
  cache wiring that these scenarios do not fully cover; see DISPOSITION-jetbridge.md.


  @VT-05
  Scenario Outline: A resource-cache placeholder refuses I/O with or without compression — <operation>
    Given a resource-cache placeholder volume
    When the placeholder is asked to "<operation>" with and without compression
    Then it reports no executor and refuses "<operation>"

    Examples:
      | operation |
      | read      |
      | write     |

  # Preserve both error paths: losing a write error lets the next step
  # believe output landed, while losing a read error conceals missing input.
  Scenario Outline: A cluster failure reaches the caller — <operation>
    Given volume "broken" sits on a cluster that cannot run commands
    When <action>
    Then it fails rather than panicking, saying "exec stream:"
    And it fails rather than panicking, saying "not found"

    Examples:
      | operation | action                                                                    |
      | read      | volume "broken" is opened then drained with and without compression         |
      | write     | a file is put into volume "broken"                                         |

  # A volume's identity is what the artifact repository keys on. A volume that
  # reported the wrong handle would hand the next step somebody else's
  # artifact — which is why this is asserted rather than assumed.
  Scenario: Volumes retain distinct database identities
    Given two persisted volumes on this worker
    Then the volumes retain their handles, worker and database rows
    # The DB row is what survives a web restart; a volume that lost it would be
    # invisible to garbage collection.

  # VT-07, and the daemon-write counterpart of the cluster-failure write row
  # above. The locator that remembers which node
  # produced an artifact lives in memory, so a web restart forgets it; when
  # daemon discovery is not configured there is then nowhere at all to send
  # the bytes. Reporting success there is the worst available outcome: the
  # step finishes green and the next one reads an empty directory.
  @VT-07
  Scenario: Writing into an artifact with no daemon to send it to fails rather than reporting success
    Given an artifact whose producing node the web has forgotten, and no way to look it up
    When the step writes its output into that artifact
    Then the write fails rather than reporting an output that never left the web

  # ==========================================================================
  # Which volumes a step is handed, and where its pod is put (CO-05, CO-06,
  # CO-10, CO-12)
  # ==========================================================================

  # Before any of the above can happen, the worker has to decide what volumes
  # a step gets and which node to ask for. Both decisions are made in
  # Worker.buildVolumeMountsForSpec and the storage backend, and the cases
  # below are the ones no scenario reached.

  # CO-05 at the worker seam. An output that lands where an input already is
  # must reuse that input's volume rather than getting a second one. Two
  # volumes on one path is not untidiness: the step's outputs are registered
  # from one list and their locations recorded from another, so the artifact
  # the next step fetches and the artifact this step wrote would be different
  # objects with different handles.
  #
  # container-pod.feature states the same rule about the POD's volumes, which
  # a different function builds. The two have to agree, and nothing had said
  # so on this side.
  # Both spellings must reuse the input: trailing slashes are common in
  # Concourse output paths and must not defeat normalization.
  @CO-05
  Scenario Outline: An overlapping output reuses its input volume — <spelling>
    Given a Kubernetes worker "mount-worker" with a database behind it
    And the worker prepares task "shared-path-handle" from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/shared"
    And it produces an output at "/tmp/build/workdir/<output>"
    When the container is created but not yet run
    Then the caller is handed these deferred volumes
      | mount path |
      | /tmp/build/workdir |
      | /tmp/build/workdir/shared |

    Examples:
      | spelling       | output  |
      | exact path     | shared  |
      | trailing slash | shared/ |

  # CO-06/CO-12. A task declares its caches relative to its working directory
  # — `caches: [{path: my-cache}]` — and Kubernetes rejects a relative
  # mountPath outright, so a cache path left unresolved is a pod the API
  # server refuses to create. Every other cache in these features is absolute,
  # so the branch that resolves one ran nowhere.
  @CO-06 @CO-12
  Scenario: A cache named relative to the working directory is mounted inside it
    Given a Kubernetes worker "mount-worker" with a database behind it
    And the worker prepares task "relative-cache-handle" from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it caches "my-cache"
    When the container is created but not yet run
    Then the caller is handed these deferred volumes
      | mount path |
      | /tmp/build/workdir |
      | /tmp/build/workdir/my-cache |

  # CO-10 now lives in artifact-recording.feature: one real-worker case
  # checks persisted inputs, majority placement and a unique positive preference.
