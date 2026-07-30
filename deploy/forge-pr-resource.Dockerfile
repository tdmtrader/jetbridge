FROM golang:1.25.6-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/forge-pr-resource ./cmd/forge-pr-resource

FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build --chmod=0555 /out/forge-pr-resource /opt/resource/check
RUN ln /opt/resource/check /opt/resource/in && ln /opt/resource/check /opt/resource/out
