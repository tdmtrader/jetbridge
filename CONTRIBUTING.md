# Contributing to JetBridge

JetBridge is a personal fork of [Concourse CI](https://github.com/concourse/concourse).
It is not the upstream project, and it does not use upstream's governance, RFC
process, Discord, or CLA -- a pull request here cannot satisfy any of them.

For the upstream project, go to
[concourse/concourse](https://github.com/concourse/concourse).

What follows is how to build, test and change this tree.

## Development process

* [Fork this repo][how-to-fork] into your GitHub account.

* Install the [development dependencies](#development-dependencies) and follow
  the instructions below for [running](#running-concourse) and
  [developing](#developing-concourse) Concourse.

* Commit your changes and push them to a branch on your fork.

  * Run [`goimports`](https://pkg.go.dev/golang.org/x/tools/cmd/goimports) to
    ensure your code follows our code style guidelines.

    * *Optional: check out our [Go style guide][style-guide]!*

* Here is a sample anatomy of an ideal commit:

    ```
     i       ii         iii
     |       |           |
    web: structure: add var declarations

    Since scripts are run in module mode, they follow the "strict mode"
    semantics. Variables must be declared prior to being assigned (e.g. - iv
    cannot have `x = 1` without declaring x (using var, let, or const)

    concourse/concourse#5131 -------------------------------------------- v

    Signed-off-by: Aidan Oldershaw <aoldershaw@pivotal.io> -------------- vi
    ```

    1. [component changed](#developing-concourse)
    1. [structure vs behaviour](#structure-and-behaviour)
    1. brief, imperative-tense description of the change
    1. a message that [tells a story][fav-commit]
    1. mention the issue this change contributes to solving
    1. [sign-off line](#signing-your-work)

* When you're ready, [submit a pull request][how-to-pr]!


### Pull request requirements

* All changes must have adequate test coverage. For instruction on writing and
  running the various test suites, see [Testing your
  changes](#testing-your-changes).

* The [documentation][docs] should be updated (in a separate, linked PR), but
  if you're not confident in your technical writing you may skip this step.


### Structure and Behaviour

In an ideal world, every pull request is small, but the codebase is large and
sometimes complex changes cannot be avoided. To ease PR reviews, there are a few
practices we've found helpful:

* Focus your commits so that they only change a single component at a time.

* Isolate [structure changes from behaviour changes][sb-changes] and label the
  commits appropriately - even better, batch commits of the same type into
  contiguous blocks.

* Give clear prose justifications for your changes in the commit messages -
  it's not unusual that you do some digging to uncover the motivation for a
  change, but if you don't mention it in the commit message the diff can feel
  pretty opaque.


## Development dependencies

You'll need a few things installed in order to build, test and run Concourse during
development:

* [`go`](https://golang.org/dl/) v1.16+
* [`git`](https://git-scm.com/) v2.11+
* [`yarn`](https://yarnpkg.com/getting-started/install) v4+
* [`postgresql`](https://www.postgresql.org/download/) server binaries on your
  `PATH` (the tests start their own postmaster)
* `docker`, `helm` and `kubectl` plus a Kubernetes cluster, to run Concourse

> *Concourse uses Go's module system, so make sure it's **not** cloned under
> your `$GOPATH`.*


## Running Concourse

Concourse runs on Kubernetes: steps are pods and artifacts live on a per-node
daemon, so there is no single-process or `docker compose` stack to bring up.
Build the image and install the chart as described under *Quick Start* in
[`README.md`](README.md) and in [`deploy/chart/README.md`](deploy/chart/README.md).
To iterate on a running cluster, rebuild the image and roll the deployment:

```sh
$ ./build.sh                          # concourse-local:latest
$ ./build.sh my-registry/concourse --push
```

`build.sh` runs `Dockerfile.build`, which compiles every binary the chart runs
and the frontend bundle inside the image build.

See [`TESTING.md`](TESTING.md) for the test tiers that create a disposable
cluster for you.

### Building `fly` and targeting your Concourse

To build and install the `fly` CLI from source, run:

```sh
$ go install ./fly
```

This will install a `fly` executable to your `$GOPATH/bin`, so make sure that's
on your `$PATH`!

Once `fly` is built, you can get a test pipeline running like this:

**Log in to your Concourse instance targeted as `dev`:**
```sh
$ fly -t dev login -c https://your-concourse.example.com -u test -p test
```

**Create an example pipeline that runs a hello world job every minute:**
[concourse/examples](https://github.com/concourse/examples) provides a collection
of example Concourse pipelines. Use its `time-triggered.yml` pipeline to create a
hello world job:

```sh
$ git clone git@github.com:concourse/examples.git
$ fly -t dev set-pipeline -p example -c examples/pipelines/time-triggered.yml
```

**Unpause the example pipeline:**
```sh
$ fly -t dev unpause-pipeline -p example
```


## Developing Concourse

Concourse's source code is structured as a monorepo containing Go source code
for the server components and Elm/Less source code for the web UI.

Currently, the top-level folders are ~~confusingly~~ cleverly named, because
they were originally separate components living in their own Git repos with
silly air-traffic-themed names.

| directory       | description |
| :-------------- | :----------- |
| `/atc`          | The "brain" of Concourse: pipeline scheduling, build tracking, resource checking, and web UI/API server. One half of `concourse web`. |
| `/fly`          | The [`fly` CLI](https://concourse-ci.org/fly.html). |
| `/web`          | The Elm source code and other assets for the web UI, which gets built and then embedded into the `concourse` executable and served by the ATC's web server. |
| `/go-concourse` | A Go client library for using the ATC API, used internally by `fly`. DO NOT try and use this in an external project, you'll end up fighting your `go.mod`. |
| `/skymarshal`   | Adapts [Dex](https://github.com/dexidp/dex) into an embeddable auth component for the ATC, plus the auth flag specifications for `fly` and `concourse web`. |
| `/cmd`          | This is mainly glue code to wire the ATC and the JetBridge runtime into the single `concourse` CLI. |
| `/topgun`       | Another acceptance suite which covers operator-level features and technical aspects of the Concourse runtime on [K8s](https://kubernetes.io/). Deploys its own Concourse clusters, runs tests against them, and tears them down. Can only be run in CI. |

### Working on a dependency

If you're working on a dependency that doesn't live under this repository (for instance,
`dex`), you'll need to update `go.mod` with a `replace` directive with the exact
reference that the module lives at:

```sh
# after pushing to the `sample` branch in your dex fork,
# try to fetch the module revision and get the version.
$ go mod download -json github.com/your-user/dex@sample | jq '.Version'
go: finding github.com/your-user/dex sample
"v0.0.0-20210401171635-fe97ad5e1a6d"

# with that version, update `go.mod` including a replace directive
$ echo 'replace github.com/concourse/dex => github.com/your-user/dex v0.0.0-20210401171635-fe97ad5e1a6d' \
  > ./go.mod

# run the usual build
$ go build ./...
```

### Working on the web UI

Concourse is written in Go, but the web UI is written in
[Elm](https://elm-lang.org) and [Less](http://lesscss.org/).

After making changes to `web/`, run the following to rebuild the web UI assets:

```sh
$ yarn install
$ yarn build
```

The served bundle is `web/public/elm.min.js`, embedded into the `concourse`
binary, so rebuild the binary (or the image) after rebuilding the assets.

### Adding migrations

Concourse database migrations live under `atc/db/migration/migrations`. They are
generated using Concourse's own inbuilt migration library. The migration file
names are of the following format:
```
<migration_version>_<migration_name>.(up|down).(sql|go)
```

The migration version number is the timestamp of the time at which the migration
files are created. This is to ensure that the migrations always run in order.
There is a utility provided to generate migration files, located at
`atc/db/migration/cli`.

To generate a migration, you have two options:

#### The short way

Use the `create-migration` script:

```sh
$ ./atc/scripts/create-migration my_migration_name
# or if you want a go migration,
$ ./atc/scripts/create-migration my_migration_name go
```

#### The long way

1. Build the CLI:
```sh
$ cd atc/db/migration
$ go build -o mig ./cli
```
2. Run the `generate` command. It takes the migration name, file type (SQL or Go)
and optionally, the directory in which to put the migration files (by default,
new migrations are placed in `./migrations`):

```sh
$ ./mig generate -n my_migration_name -t sql
```

This should generate two files for you:
```
1510262030_my_migration_name.down.sql
1510262030_my_migration_name.up.sql
```

Now that the migration files have been created in the right format, you can fill
the database up and down migrations in these files. On startup, `concourse web`
will look for any new migrations in `atc/db/migration/migrations` and will run
them in order.


## Testing your changes

Any new feature or bug fix should have tests written for it. If there are no
tests, it is unlikely that your pull request will be merged, especially if it's
for a substantial feature.

There are a few different test suites in Concourse:

* **unit tests**: Unit tests live throughout the codebase (`foo_test.go`
  alongside `foo.go`), and should probably be written for any contribution.

* `web/elm/tests/`: These test the various Elm functions in the web UI code
  in isolation. For the most part, the tests for `web/elm/src/<module name>.elm`
  will be in `web/elm/tests/<module name>Tests.elm`. We have been finding it
  helpful to test the `update` and `view` functions pretty exhaustively,
  leaving the models free to be refactored.

* `web/wats/`: This suite covers specifically the web UI, and run against a
  real Concourse cluster. This suite is still in its
  early stages and we're working out a unit testing strategy as well, so
  expectations are low for PRs, though we may provide guidance and only require
  coverage on a case-by-case basis.

* `topgun/`: This suite is more heavyweight and exercises behavior that
  may be more visible to operators than end-users. We typically do not expect
  pull requests to add to this suite.

Concourse uses [Ginkgo](http://github.com/onsi/ginkgo) as its test framework
and suite runner of choice for Go code. You'll need to install the `ginkgo` CLI
to run the Go tests:

```sh
$ go get github.com/onsi/ginkgo/ginkgo
```

Unit tests run against real collaborators: a PostgreSQL started by
`atc/postgresrunner`, real factories over it, and the production objects a
package composes with. There are no generated fakes. Where a spec has to script
a collaborator's answer -- a port whose only implementation lives behind an
architectural boundary, or an algorithm whose failure modes no healthy database
produces -- write a small double by hand in the spec file that uses it, and say
in a comment why the real object will not do.

### Running unit tests

Concourse is a ton of code, so it's faster to just run the tests for the
component you're changing.

To run the tests for the package you're in, run:

```sh
$ ginkgo -r -p
```

This will run the tests for all packages found in the current working directory,
recursively (`-r`), running all examples within each package in parallel (`-p`).

You can also pass the path to a package to run as an argument, rather than
`cd`'ing.

Note that running `go test ./...` will break, as the tests currently assume only
one package is running at a time (the `ginkgo` default). The `go test` default
is to run each package in parallel, so tests that allocate ports for test
servers and such will collide with each other.

If you want to run all the unit tests across the entire codebase, use the
following command, which skips all integration tests and linux-specific tests:

```sh
ginkgo -r -p -flake-attempts=3 \
    -skip-package topgun,fly/integration,testhelpers/otel,atc/worker/jetbridge/brine
```

If you're on a non-linux platform you may want to run the tests using Docker
instead. Some tests will fail if they aren't run on linux.

```sh
docker run --privileged -v .:/src -it --entrypoint '/bin/bash' concourse/unit
```

That will put you inside a container with the codebase mounted at `/src`. Run
the following to run all the unit tests, including the linux-specific tests:

```sh
cd /src
go install github.com/onsi/ginkgo/v2/ginkgo
ginkgo -r -p -flake-attempts=3 \
    -skip-package topgun,fly/integration,testhelpers/otel,atc/worker/jetbridge/brine
```

#### Running elm tests

You can run `yarn test` from the root of the repo or `elm-test` from the
`web/elm` directory. They are pretty snappy so you can comfortably run the
whole suite on every change.

### Elm static analysis

Running `yarn analyse` will run many checks across the codebase and report
unused imports and variables, potential optimizations, etc. Powered by
[elm-analyse](https://github.com/stil4m/elm-analyse). If you add the `-s` flag
it will run a server at `localhost:3000` which allows for easier browsing, and
even some automated fixes!

### Elm formatting

Run `yarn format` to format the elm code according to the official Elm Style
Guide. Powered by [elm-format](https://github.com/avh4/elm-format).

### Elm benchmarking

Run `yarn benchmark`.

### Running the web acceptance tests (`web/wats`)

Run `yarn test` from the `web/wats` directory. They use puppeteer to run
a headless Chromium. A handy fact is that in most cases if a test fails,
a screenshot taken at the moment of the failure will be at
`web/wats/failure.png`. There is a known
[issue](https://github.com/concourse/concourse/issues/3890) with running these
tests in parallel (which is the default setting). The issue stems from an
upstream dependency, so until it is fixed you can run `yarn test --serial` to
avoid it.

### Running Kubernetes tests

The Kubernetes runtime has two end-to-end suites, both run against an
ephemeral K3s cluster provisioned via testcontainers (Docker is the only
cluster prerequisite):

```bash
# integration suite (topgun/k8s/integration/)
make test-k8s-integration

# full behavioral suite (topgun/k8s_behavioral/)
make test-k8s-behavioral
```

See [TESTING.md](TESTING.md) for image build steps and deployed-cluster guidance; see
[CLAUDE.md](CLAUDE.md#running-tests) for test tiers and timing expectations.

[docs]: https://github.com/concourse/docs
[fav-commit]: https://dhwthompson.com/2019/my-favourite-git-commit
[how-to-fork]: https://help.github.com/articles/fork-a-repo/
[how-to-pr]: https://help.github.com/articles/creating-a-pull-request-from-a-fork/
[sb-changes]: https://medium.com/@kentbeck_7670/bs-changes-e574bc396aaa
[style-guide]: https://github.com/concourse/concourse/wiki/Concourse-Go-Style-Guide
