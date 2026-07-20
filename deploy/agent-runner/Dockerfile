FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner
RUN CGO_ENABLED=0 go build -o /out/harvest-runner ./cmd/harvest-runner

FROM node:20-bookworm-slim
# Pin to the claude-code version the live theborg review job currently runs
# (check a recent agent-review build log before bumping).
RUN npm install -g @anthropic-ai/claude-code@2.0.1 \
    && apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# NOTE: the Elm 0.19.1 + uglify-js toolchain (WF-2, for front-end tickets) was
# REMOVED from this image — the build pod's egress to external hosts is
# unreliable/blocked: the elm-binary download (github release CDN) hit
# `curl: (35) Connection reset by peer` and the ELM_HOME pre-warm could not reach
# package.elm-lang.org (builds #8/#9/#10 all failed). It only made the agent-runner
# image build flaky while UI tickets are blocked anyway. Re-add it ONLY with a
# network-independent delivery: vendor the elm linux-x64 binary in-repo (or an
# in-cluster mirror) and a pre-populated ELM_HOME, so the build needs no external
# egress. The elm-build harvest GATE tolerates a missing `elm` (it errors that
# gate, which only fires on web/elm changes — none dispatched yet).
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
# harvest-runner shares the image (§2.8.1: the harvest step's main
# container is the agent-step image; git already present above).
COPY --from=build /out/harvest-runner /usr/local/bin/harvest-runner
COPY --from=build /usr/local/go /usr/local/go
# Root, like every other step image: jetbridge hostPath step volumes are
# kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a
# non-root user gets EACCES writing the flight recorder. IS_SANDBOX=1 lets
# the claude CLI accept --dangerously-skip-permissions when running as root
# (single-tenant, pod-isolated sandbox).
ENV IS_SANDBOX=1
ENV PATH="/usr/local/go/bin:${PATH}"
ENTRYPOINT ["agent-runner"]
