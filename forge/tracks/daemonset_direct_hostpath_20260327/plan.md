# Plan: DaemonSet Direct HostPath Mounts

## Phase 1: Wire ArtifactLocator into production code

### [ ] Write test verifying locator is wired to Worker
### [ ] Write test verifying locator is wired to Reaper
### [ ] Instantiate ArtifactLocator in atccmd and wire to Worker and Reaper

## Phase 2: Direct hostPath output volumes

### [ ] Write tests for hostPath output subdirectories
### [ ] Implement hostPath output volumes in buildVolumeMounts

## Phase 3: cp -a init containers for local inputs

### [ ] Write tests for local cp -a and remote tar fetch
### [ ] Implement branching init containers (cp -a local, HTTP remote)

## Phase 4: Direct cache hostPath mounts

### [ ] Write tests for direct cache mounts
### [ ] Implement cache hostPath volumes in buildVolumeMounts

## Phase 5: Build log events for fetch source

### [ ] Write tests for fetch source logging
### [ ] Emit log events per input (local/remote/unknown)

## Phase 6: Build-aware TTL sweeper

### [ ] Write tests for build-aware sweep
### [ ] Update sweeper to check pod existence before deleting

## Phase 7: Artifact-daemon directory serving

### [ ] Write tests for directory-based tar-on-the-fly GET
### [ ] Update artifact-daemon to serve directories

## Phase 8: Validation

### [ ] Run jetbridge unit tests
### [ ] Run artifact-daemon tests
