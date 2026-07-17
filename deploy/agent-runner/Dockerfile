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
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
# harvest-runner shares the image (§2.8.1: the harvest step's main
# container is the agent-step image; git already present above).
COPY --from=build /out/harvest-runner /usr/local/bin/harvest-runner
# Root, like every other step image: jetbridge hostPath step volumes are
# kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a
# non-root user gets EACCES writing the flight recorder. IS_SANDBOX=1 lets
# the claude CLI accept --dangerously-skip-permissions when running as root
# (single-tenant, pod-isolated sandbox).
ENV IS_SANDBOX=1
ENTRYPOINT ["agent-runner"]
