FROM golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/agent-broker ./cmd/agent-broker

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS harnesses
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /downloads
RUN curl --fail --location --silent --show-error \
      https://github.com/openai/codex/releases/download/rust-v0.146.0/codex-x86_64-unknown-linux-musl.tar.gz \
      --output codex.tar.gz \
    && echo "5ba3b9405543953081f661d0854d266f76e2abbe51d41349355a36de7673776a  codex.tar.gz" | sha256sum --check --strict \
    && tar -xzf codex.tar.gz \
    && install -D -m 0555 codex-x86_64-unknown-linux-musl /out/codex
RUN curl --fail --location --silent --show-error \
      https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.212/linux-x64/claude \
      --output claude \
    && echo "044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e  claude" | sha256sum --check --strict \
    && install -D -m 0555 claude /out/claude
RUN curl --fail --location --silent --show-error \
      https://downloads.cursor.com/lab/2026.07.23-e383d2b/linux/x64/agent-cli-package.tar.gz \
      --output cursor-agent.tar.gz \
    && echo "702ad595213bee5df0268be9f80a19f29fcceaa2a42fc55e39f2b5199051f0c4  cursor-agent.tar.gz" | sha256sum --check --strict \
    && install -d -m 0555 /out/cursor-agent/2026.07.23-e383d2b \
    && tar -xzf cursor-agent.tar.gz --strip-components=1 -C /out/cursor-agent/2026.07.23-e383d2b \
    && test -x /out/cursor-agent/2026.07.23-e383d2b/cursor-agent \
    && test -x /out/cursor-agent/2026.07.23-e383d2b/node

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates git ripgrep \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build --chmod=0555 /out/agent-broker /usr/local/bin/agent-broker
COPY --from=harnesses --chown=65532:65534 --chmod=0555 /out/codex /usr/local/bin/codex
COPY --from=harnesses --chown=65532:65534 --chmod=0555 /out/claude /usr/local/bin/claude
COPY --from=harnesses --chown=65532:65534 /out/cursor-agent/2026.07.23-e383d2b /opt/cursor-agent/2026.07.23-e383d2b
RUN ln -s /opt/cursor-agent/2026.07.23-e383d2b/cursor-agent /usr/local/bin/cursor-agent \
    && find /opt/cursor-agent/2026.07.23-e383d2b -type d -exec chmod 0555 {} + \
    && find /opt/cursor-agent/2026.07.23-e383d2b -type f -exec chmod a-w {} +
COPY --chown=65532:65534 --chmod=0444 deploy/agent-broker/assets/ /opt/concourse/agent-broker/
ENV HOME=/scratch
USER 65532:65534
WORKDIR /
ENTRYPOINT ["/usr/local/bin/agent-broker"]
