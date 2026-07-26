# The deterministic stand-in agent image used by the behavioral suite and by
# any operator who wants to exercise the agent-step path without spending real
# model tokens. It is NEVER a release artifact.
#
# busybox, not scratch: jetbridge runs every supervised process as
# `sh -c <supervisor script>` (atc/worker/jetbridge/supervisor.go), which needs
# sh plus cat/sed/cut/tail/mkdir/mv/sleep.
#
# Unlike cmd/oom-trigger's helper (built on the host with a hardcoded
# GOOS=linux GOARCH=amd64), this build stage compiles natively inside the
# image, so it can't drift from whatever architecture the build actually
# targets. The trade-off is a slower first build: this stage pulls and runs a
# full Go toolchain image instead of copying in an already-built host binary.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/fixture-agent ./cmd/fixture-agent

FROM busybox:1.37.0
# Installed under the name atc/exec/agent_step.go execs. Renaming this breaks
# the behavioral tier with a bare "executable file not found".
COPY --from=build /out/fixture-agent /usr/local/bin/agent-runner
ENTRYPOINT ["agent-runner"]
