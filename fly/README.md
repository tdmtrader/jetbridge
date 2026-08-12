# fly

The Concourse command line interface.

[The upstream documentation](https://concourse-ci.org/fly.html) applies — this
fork does not change the `fly` CLI, the pipeline YAML it submits, or the REST
API it talks to.

## Building

From a checkout of this repository:

```bash
cd fly
go build
```

Tests use [ginkgo](https://onsi.github.io/ginkgo/):

```bash
go install github.com/onsi/ginkgo/v2/ginkgo
ginkgo -r
```

The integration suite, which builds the binary and runs it against a mock ATC,
is `make test-fly-integration` from the repository root. See
[TESTING.md](../TESTING.md).

## Installing from the Concourse UI

`fly` is available for download from the lower right-hand corner of the web UI,
which links to `/download-fly`.

## Upgrading fly

`fly` is not upgraded independently of the ATC it talks to. Either download it
from the web UI again, or run `fly -t <target> sync`.

Note that `fly` hard-errors on a MAJOR.MINOR version mismatch against the ATC
(a patch difference only warns), so `fly sync` is the in-band remedy after the
cluster moves to a new minor version.
