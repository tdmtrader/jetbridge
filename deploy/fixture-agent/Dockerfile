# The deterministic stand-in agent image used by the behavioral suite and by
# any operator who wants to exercise the agent-step path without spending real
# model tokens. It is NEVER a release artifact.
#
# busybox, not scratch: jetbridge runs every supervised process as
# `sh -c <supervisor script>` (atc/worker/jetbridge/supervisor.go), which needs
# sh plus cat/sed/cut/tail/mkdir/mv/sleep.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/fixture-agent ./cmd/fixture-agent

FROM busybox:1.37.0
# Installed under the name atc/exec/agent_step.go execs. Renaming this breaks
# the behavioral tier with a bare "executable file not found".
COPY --from=build /out/fixture-agent /usr/local/bin/agent-runner
ENTRYPOINT ["agent-runner"]
