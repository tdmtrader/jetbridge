# Plan: DaemonSet Direct HostPath Mounts

## Phase 1: Wire ArtifactLocator into production code

### [x] Instantiate ArtifactLocator in atccmd b64f9e4c5
### [x] Wire to Worker factory and Reaper b64f9e4c5

## Phase 2: HostPath output and dir volumes

### [x] Write tests for hostPath output volumes ad51ee896
### [x] Implement hostPath volumes in buildVolumeMounts ad51ee896

## Phase 3: cp -a init containers for local inputs

### [x] Write tests for cp -a local and HTTP remote fetch ad51ee896
### [x] Implement cp -a init containers ad51ee896

## Phase 4: Direct cache hostPath mounts

### [x] Write tests for direct cache mounts ad51ee896
### [x] Implement cache hostPath volumes ad51ee896

## Phase 5: Artifact-daemon directory serving

### [x] Write tests for directory-based GET dd47ea024
### [x] Implement directory serving dd47ea024

## Phase 6: Build-aware TTL sweeper

### [x] Write tests for build-aware sweep dd47ea024
### [x] Update sweeper to sweep /steps/ directories only dd47ea024

## Phase 7: Reaper URL update

### [x] Update reaper DELETE URL for directory paths dd47ea024

## Phase 8: Validation

### [x] All jetbridge tests pass (373+ Ginkgo specs + 22 Go tests)
### [x] All artifact-daemon tests pass (18 tests)
### [x] All worker factory tests pass
### [x] go build and go vet pass
