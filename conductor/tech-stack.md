# Tech Stack

## Languages

| Language | Version | Purpose |
|----------|---------|---------|
| Go | 1.25.0 | Backend, CLI (`fly`), worker runtime, K8s runtime |
| Elm | 0.19.1 | Web UI frontend |
| JavaScript | ES2015+ | Build tooling, Webpack config |
| SQL | PostgreSQL 13+ | Database migrations, queries |
| Less | 3.x | CSS preprocessing for web UI |

## Backend (Go)

### Core Framework & Libraries
- **Database:** `jackc/pgx/v5` (PostgreSQL driver), `Masterminds/squirrel` (SQL builder)
- **HTTP:** Standard library `net/http`, `gorilla/websocket`
- **CLI:** `jessevdk/go-flags` (argument parsing)
- **Logging:** `code.cloudfoundry.org/lager/v3` (structured logging)
- **Config:** `caarlos0/env/v11` (environment variable parsing)
- **YAML:** `goccy/go-yaml` (pipeline parsing)

### Kubernetes Integration
- **Client:** `k8s.io/client-go` (Kubernetes API client)
- **API Types:** `k8s.io/api`, `k8s.io/apimachinery`
- **Dynamic Client:** For CRD and unstructured resource access

### Observability
- **Tracing:** `go.opentelemetry.io/otel` (OpenTelemetry SDK)
- **Trace Export:** Google Cloud Trace exporter, OTLP exporter
- **Metrics:** `prometheus/client_golang`, `DataDog/datadog-go/v5`, `influxdata/influxdb1-client`

### Authentication & Secrets
- **OAuth/OIDC:** `concourse/dex` (identity provider)
- **Vault:** `hashicorp/vault/api`
- **AWS:** `aws/aws-sdk-go-v2` (SSM, Secrets Manager)
- **Conjur:** `cyberark/conjur-api-go`
- **CredHub:** `code.cloudfoundry.org/credhub-cli`

### Testing
- **Framework:** `onsi/ginkgo/v2` + `onsi/gomega` (BDD-style)
- **Mocking:** `maxbrunsfeld/counterfeiter/v6`
- **HTTP Testing:** Standard library `net/http/httptest`

## Frontend (Elm + JS)

- **UI Framework:** Elm 0.19.1
- **Bundler:** Webpack 5
- **CSS:** Less with autoprefixer
- **Icons:** Material Design Icons (MDI)
- **Browser Testing:** Puppeteer

## Infrastructure

- **Container Runtime:** Kubernetes (JetBridge), containerd (legacy/upstream)
- **Database:** PostgreSQL 13+
- **Local Dev:** Docker Compose
- **CI:** Concourse pipelines (self-hosted)
- **Deployment:** Docker images, Kubernetes Helm, BOSH releases

## Repository Structure (Key Directories)

```
atc/           — Air Traffic Control: API server, scheduler, engine, DB layer
cmd/           — Binary entry points (concourse, concourse-mcp, init)
fly/           — CLI tool source
worker/        — Worker process and runtime backends
  worker/kubernetes/  — JetBridge K8s runtime implementation
skymarshal/    — Authentication & authorization
web/           — Web UI (Elm frontend)
vars/          — Variable/secret resolution
tracing/       — Distributed tracing utilities
go-concourse/  — Go client library
testflight/    — Integration test suite
topgun/        — E2E test suite
integration/   — Additional integration tests
```

## Build & Run

```bash
# Backend
go build ./cmd/concourse

# Frontend
cd web && npm install && npm run build

# Local development
docker-compose up

# Tests
go test ./...                    # All Go tests
ginkgo -r ./worker/kubernetes/   # K8s runtime tests
cd web/elm && elm-test           # Frontend tests
```
