# Learnings

### 2026-03-28 [frustration]

[2026-03-28] Live volume passing tests (TestLiveVolumePassingGetToTask, etc.) broken since PVC/SPDY deprecation commit 1b3972e89. The daemon resolves artifacts successfully (confirmed via logs) but the main container still can't find the data. Suspect the daemon's cp -a copies to the hostPath directory but the init container that calls /resolve runs inside the pod — it doesn't have write access to the hostPath dest because the init container mounts the artifact-daemon-hostpath volume as read-only. The actual input volume (input-1) is a separate hostPath mount. The daemon copies to the input-1 hostPath from outside the pod, which should work since both are on the same node. Need deeper debugging — possibly a timing issue or the daemon is copying to the wrong subdirectory within the hostPath.
