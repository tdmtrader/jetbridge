package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
)

// ResourceCheckingDefinitions migrates atc/lidar/scanner_test.go — the
// component that decides, once a tick, which resources and resource types the
// cluster is going to look at.
//
// THE ONE DOUBLE IN THIS FILE, AND WHY IT ANSWERS RATHER THAN RECORDS.
//
// The scanner is constructed with an imageresolver.Resolver, and the ginkgo
// suite passed a counterfeiter FakeResolver: it recorded its arguments and the
// tests asserted ResolveCallCount() and ResolveArgsForCall(0). That is a call
// count, twice over, and it is also weaker than it looks — a scanner that
// resolved the right repository and then persisted somebody else's digest
// would pass every one of those assertions.
//
// imageRegistry below is a working double instead. It holds images at
// (repository, tag) and answers with the digest it holds, refusing what it
// does not hold and refusing a private image unless the credentials match. So
// every assertion in the feature file is on a digest that reached the
// database, and the digest identifies WHICH image was asked for: seed two
// repositories with two digests and the persisted digest says which one the
// scan resolved. The credential test is the same trick — a wrong password
// means no digest, so "the digest landed" IS the assertion that the
// credentials arrived intact.
//
// A REAL registry was the other option and is nearly available: go-containerregistry
// ships an in-process one (pkg/registry), and atc/imageresolver/resolver_test.go
// already runs the production resolver against it over httptest. It is not
// used here only because this module has its own go.mod and reaching it needs
// a require line, which is outside what this migration is allowed to touch.
// Nothing else stands in the way, and it is the obvious next strengthening:
// it would put atc/imageresolver's HTTP path under these scenarios too.
//
// THE OTHER DOUBLES ARE FAULTS, NOT ANSWERS.
//
// Three failures are injected, and none of them fabricates an error:
//
//   - "the database has gone away" builds the check factory over a connection
//     that has been closed, exactly as the gc pilot does. The refusal is
//     PostgreSQL's.
//   - "the resource types table has been renamed" renames a real table, so the
//     second of the scanner's two enumerations fails against real PostgreSQL
//     while the first still succeeds. There is no way to fail one and not the
//     other through a closed connection, and this needs no wrapper at all.
//   - "the garbage collector deletes the scope" DELETES THE ROW. The
//     FK violation that follows is raised by PostgreSQL, on the real
//     constraint, at the real moment. The ginkgo suite constructed a
//     *pgconn.PgError with SQLSTATE 23503 by hand; a scanner that classified
//     on a string rather than on the driver error would have passed that.
//
// WHAT THE TABLES ARE READ FOR. Every outcome here is a row: the digest on the
// resource's scope, whether a scope was ever attached, the last-check time
// that decides whether the next tick bothers, and the check build the scanner
// pushed onto the channel the build tracker reads. Nothing counts calls.

// -----------------------------------------------------------------------
// The registry
// -----------------------------------------------------------------------

// imageRegistry is an OCI registry that answers. It is the seam the scanner
// takes when it resolves an image natively instead of scheduling a check pod.
type imageRegistry struct {
	mu      sync.Mutex
	images  map[string]registryImage
	crashes map[string]bool
}

type registryImage struct {
	digest string
	// username empty means the image is public. A private image is refused
	// unless the credentials the scanner carried match exactly, which is what
	// makes "the digest landed" a statement about credentials.
	username string
	password string
}

func newImageRegistry() *imageRegistry {
	return &imageRegistry{
		images:  map[string]registryImage{},
		crashes: map[string]bool{},
	}
}

func (r *imageRegistry) hold(ref, digest, username, password string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images[canonicalRef(ref)] = registryImage{digest: digest, username: username, password: password}
}

// crashOn arms a panic for one reference. Resolve answers the crash BEFORE it
// looks the image up, so a reference can be both held and crashing — which is
// what the crash scenario needs: an image the registry would otherwise resolve
// is the only way "left unresolved" can witness the panic rather than merely
// re-stating that the registry does not hold it.
func (r *imageRegistry) crashOn(ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.crashes[canonicalRef(ref)] = true
}

// Resolve mirrors imageresolver.registryResolver's contract, including both of
// its edge rules: an empty tag means "latest", and an empty repository is
// refused before anything else happens. Both belong to production and are
// pinned there — atc/imageresolver/resolver_test.go's TestResolver_DefaultTag
// and TestResolver_EmptyRepository — and are repeated here so a scenario can
// leave the tag off a reference the way a pipeline author does, and so a
// scenario cannot accidentally be written over a registry more permissive than
// the real one.
//
// The empty-repository refusal is also why no scenario in this file exercises
// lidar's own `repository == ""` guard: production refuses at this same line,
// so the guard changes nothing observable, and a double that answered for the
// empty repository would only manufacture a state no registry can reach. See
// the last DISPOSITION in features/resource-checking.feature.
func (r *imageRegistry) Resolve(_ context.Context, repository, tag string, auth *imageresolver.BasicAuth) (string, error) {
	if repository == "" {
		return "", fmt.Errorf("empty repository")
	}
	ref := canonicalRef(repository + ":" + tag)

	r.mu.Lock()
	image, held := r.images[ref]
	crash := r.crashes[ref]
	r.mu.Unlock()

	if crash {
		panic("the registry crashed answering for " + ref)
	}
	if !held {
		return "", fmt.Errorf("resolving digest for %q: MANIFEST_UNKNOWN", ref)
	}
	if image.username != "" {
		if auth == nil {
			return "", fmt.Errorf("resolving digest for %q: UNAUTHORIZED, no credentials were offered", ref)
		}
		if auth.Username != image.username || auth.Password != image.password {
			return "", fmt.Errorf("resolving digest for %q: UNAUTHORIZED, wrong credentials", ref)
		}
	}
	return image.digest, nil
}

func splitRef(ref string) (string, string) {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

func canonicalRef(ref string) string {
	repository, tag := splitRef(ref)
	if tag == "" {
		tag = "latest"
	}
	return repository + ":" + tag
}

// refSource is the pipeline source a scenario's "reading <ref>" produces. The
// tag is only written when the scenario gave one, so a reference with no tag
// exercises the empty-tag path rather than quietly filling it in.
func refSource(ref string) atc.Source {
	repository, tag := splitRef(ref)
	source := atc.Source{"repository": repository}
	if tag != "" {
		source["tag"] = tag
	}
	return source
}

// -----------------------------------------------------------------------
// The state
// -----------------------------------------------------------------------

// pipelineDraft accumulates a pipeline across the Given steps and is saved
// once, the first time a step needs real rows.
type pipelineDraft struct {
	TeamName string
	Name     string
	Config   atc.Config
	Inputs   []string
	Outputs  []string

	Team     db.Team
	Pipeline db.Pipeline
	dirty    bool
}

func (d *pipelineDraft) jobs() atc.JobConfigs {
	if len(d.Inputs) == 0 && len(d.Outputs) == 0 {
		return nil
	}
	steps := make([]atc.Step, 0, len(d.Inputs)+len(d.Outputs))
	for _, name := range d.Inputs {
		steps = append(steps, atc.Step{Config: &atc.GetStep{Name: name}})
	}
	for _, name := range d.Outputs {
		steps = append(steps, atc.Step{Config: &atc.PutStep{Name: name}})
	}
	return atc.JobConfigs{{Name: "scan-job", PlanSequence: steps}}
}

// ScanReady is the pipeline a scan is about to run over, plus whatever has
// been arranged to go wrong.
type ScanReady struct {
	DB       JetbridgeDB
	Registry *imageRegistry

	// Resolver is a nil INTERFACE when lidar was started without one, which is
	// the switch the scanner branches on. A typed nil would not be.
	Resolver imageresolver.Resolver
	Workers  int

	CheckBuilds chan db.Build

	// CheckFactory is built ONCE and reused by every scan in the scenario,
	// because in production it is: atccmd constructs one and the component
	// runner calls the same instance every tick. It carries the in-flight
	// check set in a field, so a scenario that built a fresh one per scan
	// would have every pass believe nothing was running — which is exactly the
	// state the duplicate-suppression exists to prevent, made unobservable.
	CheckFactory  db.CheckFactory
	ConfigFactory db.ResourceConfigFactory
	Logger        *lagertest.TestLogger

	Pipelines []*pipelineDraft
	current   int

	// Faults.
	scopeVanishes     string
	faultActive       *bool
	brokenConn        bool
	typesTableRenamed bool
}

// ScanDone is what one or more scans left behind.
type ScanDone struct {
	Ready  ScanReady
	Err    error
	Builds []db.Build
	Delta  int
}

func ResourceCheckingDefinitions() []brine.StepDefinition {
	defs := scanSetupDefinitions()
	defs = append(defs, scanFixtureDefinitions()...)
	defs = append(defs, scanRunDefinitions()...)
	defs = append(defs, scanOutcomeDefinitions()...)
	defs = append(defs, checkPlanDefinitions()...)
	return defs
}

// -----------------------------------------------------------------------
// Given: the scan, the registry, the pipeline
// -----------------------------------------------------------------------

func scanSetupDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		newScan("a lidar scan that was given no image resolver", false),
		newScan("a lidar scan backed by an image registry", true),

		Refine[ScanReady]("the scan runs {int} resources at a time",
			func(in ScanReady, a Args) ScanReady {
				in.Workers = a.Int(0)
				return in
			}),

		Refine[ScanReady]("the registry holds {string} at the digest {string}",
			func(in ScanReady, a Args) ScanReady {
				in.Registry.hold(a.String(0), a.String(1), "", "")
				return in
			}),

		Refine[ScanReady]("the registry holds {string} at the digest {string} behind the login {string} and the password {string}",
			func(in ScanReady, a Args) ScanReady {
				in.Registry.hold(a.String(0), a.String(1), a.String(2), a.String(3))
				return in
			}),

		// The panic seam. util.DumpPanic guards one scan unit at a time; a
		// registry that crashes is the only way to reach it without wrapping
		// production, and the crash lands inside exactly the same recover.
		//
		// Pair this with a "holds" step for the SAME reference. Without one the
		// registry refuses the image anyway, the scan leaves the same row
		// untouched whether the panic fired or not, and the scenario asserts
		// nothing about the crash.
		Refine[ScanReady]("the registry crashes when asked for {string}",
			func(in ScanReady, a Args) ScanReady {
				in.Registry.crashOn(a.String(0))
				return in
			}),

		Refine[ScanReady]("everything after this is on a second pipeline in another team",
			func(in ScanReady, _ Args) ScanReady {
				in.Pipelines = append(in.Pipelines, &pipelineDraft{
					TeamName: fmt.Sprintf("scanner-team-%d", len(in.Pipelines)+1),
					Name:     fmt.Sprintf("scanner-pipeline-%d", len(in.Pipelines)+1),
					dirty:    true,
				})
				in.current = len(in.Pipelines) - 1
				return in
			}),

		// An ordinary resource: a base type, read by a job. This is the shape
		// that goes to a check pod.
		Refine[ScanReady]("the pipeline has the resource {string}",
			func(in ScanReady, a Args) ScanReady {
				name := a.String(0)
				in.addResource(atc.ResourceConfig{
					Name:   name,
					Type:   dbtest.BaseResourceType,
					Source: atc.Source{"repository": name},
				}, true)
				return in
			}),

		// Put-only: written by a job, read by none. Resources() treats it
		// differently once its last check succeeded, and that difference is
		// what stops a cluster burning check pods on outputs.
		Refine[ScanReady]("the pipeline has the resource {string} written by a job but never read",
			func(in ScanReady, a Args) ScanReady {
				name := a.String(0)
				in.addResource(atc.ResourceConfig{
					Name:   name,
					Type:   dbtest.BaseResourceType,
					Source: atc.Source{"repository": name},
				}, false)
				return in
			}),

		Refine[ScanReady]("the pipeline has the resource {string} of the custom type {string}",
			func(in ScanReady, a Args) ScanReady {
				name := a.String(0)
				in.addResource(atc.ResourceConfig{
					Name:   name,
					Type:   a.String(1),
					Source: atc.Source{"repository": name},
				}, true)
				return in
			}),

		// A custom resource type sitting on a base type: the parent whose own
		// image has to be checked and fetched before the resource beneath it
		// can be checked at all.
		Refine[ScanReady]("the pipeline has the custom resource type {string} reading {string} tagged {string}",
			func(in ScanReady, a Args) ScanReady {
				in.addResourceType(atc.ResourceType{
					Name:   a.String(0),
					Type:   dbtest.BaseResourceType,
					Source: atc.Source{"repository": a.String(1)},
					Tags:   parseTags(a.String(2)),
				})
				return in
			}),

		Refine[ScanReady]("the pipeline has the image resource {string} reading {string}",
			func(in ScanReady, a Args) ScanReady {
				in.addResource(atc.ResourceConfig{
					Name:   a.String(0),
					Type:   "registry-image",
					Source: refSource(a.String(1)),
				}, true)
				return in
			}),

		Refine[ScanReady]("the pipeline has the resource type {string} reading {string}",
			func(in ScanReady, a Args) ScanReady {
				in.addResourceType(atc.ResourceType{
					Name:   a.String(0),
					Type:   "registry-image",
					Source: refSource(a.String(1)),
				})
				return in
			}),

		Refine[ScanReady]("the pipeline has {int} resources",
			func(in ScanReady, a Args) ScanReady {
				for i := range a.Int(0) {
					name := fmt.Sprintf("resource-%02d", i)
					in.addResource(atc.ResourceConfig{
						Name:   name,
						Type:   dbtest.BaseResourceType,
						Source: atc.Source{"index": fmt.Sprintf("%02d", i)},
					}, true)
				}
				return in
			}),

		// Refinements to something already declared.

		Refine[ScanReady]("the resource {string} carries the tags {string}",
			func(in ScanReady, a Args) ScanReady {
				in.editResource(a.String(0), func(r *atc.ResourceConfig) { r.Tags = parseTags(a.String(1)) })
				return in
			}),

		Refine[ScanReady]("the resource {string} times out after {string}",
			func(in ScanReady, a Args) ScanReady {
				in.editResource(a.String(0), func(r *atc.ResourceConfig) { r.CheckTimeout = a.String(1) })
				return in
			}),

		refineCheckEvery("the resource {string} is checked every {string}", false),
		refineCheckEvery("the resource type {string} is checked every {string}", true),

		Refine[ScanReady]("the resource {string} is checked never",
			func(in ScanReady, a Args) ScanReady {
				in.editResource(a.String(0), func(r *atc.ResourceConfig) { r.CheckEvery = &atc.CheckEvery{Never: true} })
				return in
			}),

		Refine[ScanReady]("the resource type {string} is checked never",
			func(in ScanReady, a Args) ScanReady {
				in.editResourceType(a.String(0), func(r *atc.ResourceType) { r.CheckEvery = &atc.CheckEvery{Never: true} })
				return in
			}),

		// There is deliberately no "has no repository in its source" step. It
		// existed, two outline rows used it, and both rows have been removed:
		// scanner.go's `repository == ""` guard cannot be witnessed from here
		// because imageresolver.Resolve refuses the empty repository on its own
		// first line, so the guard changes no row and no scenario written
		// through this door can redden for it. The reasoning and the measurement
		// are the last DISPOSITION in features/resource-checking.feature. Anyone
		// re-adding the step should read it first.

		Refine[ScanReady]("the resource {string} reads {string} instead",
			func(in ScanReady, a Args) ScanReady {
				in.editResource(a.String(0), func(r *atc.ResourceConfig) { r.Source = refSource(a.String(1)) })
				return in
			}),

		Refine[ScanReady]("the resource type {string} reads {string} instead",
			func(in ScanReady, a Args) ScanReady {
				in.editResourceType(a.String(0), func(r *atc.ResourceType) { r.Source = refSource(a.String(1)) })
				return in
			}),

		// The only skip a resource type has that a resource does not: a type
		// whose image is written out in full is already pinned, so there is
		// nothing for a registry to answer.
		Refine[ScanReady]("the resource type {string} names its image directly as {string}",
			func(in ScanReady, a Args) ScanReady {
				in.editResourceType(a.String(0), func(r *atc.ResourceType) { r.Image = a.String(1) })
				return in
			}),

		Refine[ScanReady]("the resource {string} signs in as {string} with the password {string}",
			func(in ScanReady, a Args) ScanReady {
				in.editResource(a.String(0), func(r *atc.ResourceConfig) {
					r.Source["username"] = a.String(1)
					r.Source["password"] = a.String(2)
				})
				return in
			}),

		Refine[ScanReady]("the resource type {string} signs in as {string} with the password {string}",
			func(in ScanReady, a Args) ScanReady {
				in.editResourceType(a.String(0), func(r *atc.ResourceType) {
					r.Source["username"] = a.String(1)
					r.Source["password"] = a.String(2)
				})
				return in
			}),
	}
}

func newScan(pattern string, withRegistry bool) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, ScanReady](
		pattern,
		[]string{"jetbridge-db"},
		func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ScanReady, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return ScanReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
			}

			active := true
			checkBuilds := make(chan db.Build, 128)
			ready := ScanReady{
				DB:          database,
				Registry:    newImageRegistry(),
				Workers:     10,
				CheckBuilds: checkBuilds,
				CheckFactory: db.NewCheckFactory(
					database.Conn, database.LockFactory, checkBuilds, util.NewSequenceGenerator(1)),
				ConfigFactory: db.NewResourceConfigFactory(database.Conn, database.LockFactory),
				Logger:        lagertest.NewTestLogger("lidar"),
				faultActive:   &active,
				Pipelines: []*pipelineDraft{{
					TeamName: "scanner-team",
					Name:     "scanner-pipeline",
					dirty:    true,
				}},
			}
			if withRegistry {
				ready.Resolver = ready.Registry
			}
			return ready, nil
		},
	)
}

func refineCheckEvery(pattern string, isType bool) brine.StepDefinition {
	return brine.DefineMap[ScanReady, ScanReady](pattern,
		func(in ScanReady, p brine.Params, _ *brine.Recorder) (ScanReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return ScanReady{}, err
			}
			raw, err := paramAt(pattern, p, 1)
			if err != nil {
				return ScanReady{}, err
			}
			interval, err := time.ParseDuration(raw)
			if err != nil {
				return ScanReady{}, fmt.Errorf("step %q: %q is not a duration: %w", pattern, raw, err)
			}
			if isType {
				in.editResourceType(name, func(r *atc.ResourceType) { r.CheckEvery = &atc.CheckEvery{Interval: interval} })
			} else {
				in.editResource(name, func(r *atc.ResourceConfig) { r.CheckEvery = &atc.CheckEvery{Interval: interval} })
			}
			return in, nil
		},
	)
}

func parseTags(raw string) atc.Tags {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	tags := make(atc.Tags, 0, len(parts))
	for _, p := range parts {
		tags = append(tags, strings.TrimSpace(p))
	}
	return tags
}

func (r *ScanReady) draft() *pipelineDraft {
	return r.Pipelines[r.current]
}

func (r *ScanReady) addResource(config atc.ResourceConfig, readByAJob bool) {
	d := r.draft()
	d.Config.Resources = append(d.Config.Resources, config)
	if readByAJob {
		d.Inputs = append(d.Inputs, config.Name)
	} else {
		d.Outputs = append(d.Outputs, config.Name)
	}
	d.dirty = true
}

func (r *ScanReady) addResourceType(config atc.ResourceType) {
	d := r.draft()
	d.Config.ResourceTypes = append(d.Config.ResourceTypes, config)
	d.dirty = true
}

// editResource and editResourceType reach across every declared pipeline, so a
// refinement written after the "second pipeline" marker can still adjust
// something declared before it. A name nobody declared is left to fail at the
// assertion, which names the pipeline contents.
func (r *ScanReady) editResource(name string, apply func(*atc.ResourceConfig)) {
	for _, d := range r.Pipelines {
		for i := range d.Config.Resources {
			if d.Config.Resources[i].Name == name {
				apply(&d.Config.Resources[i])
				d.dirty = true
			}
		}
	}
}

func (r *ScanReady) editResourceType(name string, apply func(*atc.ResourceType)) {
	for _, d := range r.Pipelines {
		for i := range d.Config.ResourceTypes {
			if d.Config.ResourceTypes[i].Name == name {
				apply(&d.Config.ResourceTypes[i])
				d.dirty = true
			}
		}
	}
}

// persist writes every dirty draft. It is called by the scan and by any Given
// that needs real rows before the scan runs, so the order of the Givens in a
// scenario is the order the reader sees rather than a constraint they have to
// know about.
func (r ScanReady) persist() error {
	for _, d := range r.Pipelines {
		if !d.dirty {
			continue
		}
		if d.Team == nil {
			team, err := r.DB.TeamFactory.CreateTeam(atc.Team{Name: d.TeamName})
			if err != nil {
				return fmt.Errorf("create team %q: %w", d.TeamName, err)
			}
			d.Team = team
		}
		version := db.ConfigVersion(0)
		if d.Pipeline != nil {
			version = d.Pipeline.ConfigVersion()
		}
		config := d.Config
		config.Jobs = d.jobs()

		pipeline, _, err := d.Team.SavePipeline(atc.PipelineRef{Name: d.Name}, config, version, false)
		if err != nil {
			return fmt.Errorf("save pipeline %q: %w", d.Name, err)
		}
		d.Pipeline = pipeline
		d.dirty = false
	}
	return nil
}

// findResource and findResourceType look a name up across every pipeline and
// refuse an ambiguous one, so a fixture that stopped inserting is reported
// where it happened rather than as a mysterious absence three steps later.
func (r ScanReady) findResource(name string) (db.Resource, error) {
	var found db.Resource
	for _, d := range r.Pipelines {
		if d.Pipeline == nil {
			continue
		}
		resource, ok, err := d.Pipeline.Resource(name)
		if err != nil {
			return nil, fmt.Errorf("look up the resource %q: %w", name, err)
		}
		if !ok {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("more than one pipeline has a resource named %q", name)
		}
		found = resource
	}
	if found == nil {
		return nil, fmt.Errorf("no pipeline in this scenario has a resource named %q", name)
	}
	return found, nil
}

func (r ScanReady) findResourceType(name string) (db.ResourceType, error) {
	var found db.ResourceType
	for _, d := range r.Pipelines {
		if d.Pipeline == nil {
			continue
		}
		resourceType, ok, err := d.Pipeline.ResourceType(name)
		if err != nil {
			return nil, fmt.Errorf("look up the resource type %q: %w", name, err)
		}
		if !ok {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("more than one pipeline has a resource type named %q", name)
		}
		found = resourceType
	}
	if found == nil {
		return nil, fmt.Errorf("no pipeline in this scenario has a resource type named %q", name)
	}
	return found, nil
}

// -----------------------------------------------------------------------
// Given: rows, and things arranged to go wrong
// -----------------------------------------------------------------------

func scanFixtureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// A resource that has already had a successful check, set up by the
		// same dbtest builder the atc/db suites use — so the rows are the ones
		// a real check leaves, not an approximation of them.
		brine.DefineMap[ScanReady, ScanReady](
			"the resource {string} has already been checked successfully",
			func(in ScanReady, p brine.Params, _ *brine.Recorder) (ScanReady, error) {
				pattern := "the resource {string} has already been checked successfully"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return ScanReady{}, err
				}
				if err := in.persist(); err != nil {
					return ScanReady{}, err
				}
				d := in.draft()
				scenario := &dbtest.Scenario{Team: d.Team, Pipeline: d.Pipeline}
				setup := in.DB.Builder.WithResourceVersions(name, atc.Version{"ref": "already-checked"})
				if err := setup(scenario); err != nil {
					return ScanReady{}, fmt.Errorf("give %q a successful check: %w", name, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[ScanReady, ScanReady](
			"the resource {string} is pinned to the version {string}",
			func(in ScanReady, p brine.Params, _ *brine.Recorder) (ScanReady, error) {
				pattern := "the resource {string} is pinned to the version {string}"
				name, raw, err := twoParams(pattern, p)
				if err != nil {
					return ScanReady{}, err
				}
				version, err := parseVersionText(raw)
				if err != nil {
					return ScanReady{}, err
				}
				if err := in.persist(); err != nil {
					return ScanReady{}, err
				}
				d := in.draft()
				scenario := &dbtest.Scenario{Team: d.Team, Pipeline: d.Pipeline}
				if err := in.DB.Builder.WithResourceVersions(name, version)(scenario); err != nil {
					return ScanReady{}, fmt.Errorf("save the version to pin for %q: %w", name, err)
				}
				resource, err := in.findResource(name)
				if err != nil {
					return ScanReady{}, err
				}
				persisted, found, err := resource.FindVersion(version)
				if err != nil {
					return ScanReady{}, fmt.Errorf("find the version to pin for %q: %w", name, err)
				}
				if !found {
					return ScanReady{}, fmt.Errorf("the version to pin for %q was not saved", name)
				}
				changed, err := resource.PinVersion(persisted.ID())
				if err != nil {
					return ScanReady{}, fmt.Errorf("pin %q: %w", name, err)
				}
				if !changed {
					return ScanReady{}, fmt.Errorf("pinning %q changed nothing", name)
				}
				return in, nil
			},
		),

		// The in-flight fixture: a check build that was created and never
		// finished, which is what a check taking longer than the scan interval
		// looks like. The build is drained off the channel here so the scan's
		// own output is the only thing left on it.
		brine.DefineMap[ScanReady, ScanReady](
			"a check for the resource {string} is already in flight",
			func(in ScanReady, p brine.Params, _ *brine.Recorder) (ScanReady, error) {
				pattern := "a check for the resource {string} is already in flight"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return ScanReady{}, err
				}
				if err := in.persist(); err != nil {
					return ScanReady{}, err
				}
				resource, err := in.findResource(name)
				if err != nil {
					return ScanReady{}, err
				}
				// The in-flight guard keys on the config scope, so the
				// resource must have one before it can be tracked at all.
				config, err := in.ConfigFactory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
				if err != nil {
					return ScanReady{}, fmt.Errorf("find or create the config for %q: %w", name, err)
				}
				id := resource.ID()
				scope, err := config.FindOrCreateScope(&id)
				if err != nil {
					return ScanReady{}, fmt.Errorf("find or create the scope for %q: %w", name, err)
				}
				if err := resource.SetResourceConfigScope(scope); err != nil {
					return ScanReady{}, fmt.Errorf("attach the scope to %q: %w", name, err)
				}
				resource, err = in.findResource(name)
				if err != nil {
					return ScanReady{}, err
				}

				// The SAME factory the scan will use, because the in-flight set
				// lives on the instance. A second factory here would leave the
				// scan with nothing to suppress.
				_, created, err := in.CheckFactory.TryCreateCheck(
					lagerctx.NewContext(context.Background(), in.Logger),
					resource, nil, nil, false, false, false)
				if err != nil {
					return ScanReady{}, fmt.Errorf("start the in-flight check for %q: %w", name, err)
				}
				if !created {
					return ScanReady{}, fmt.Errorf("the in-flight check for %q was not created", name)
				}
				// Deliberately NOT finished: an unfinished build is what keeps
				// it in flight. Drain it so it is not mistaken for the scan's.
				if drained := drainChecks(in.CheckBuilds); len(drained) != 1 {
					return ScanReady{}, fmt.Errorf("expected 1 in-flight check build for %q, got %d", name, len(drained))
				}
				return in, nil
			},
		),

		brine.DefineMap[ScanReady, ScanReady](
			"the database the scan reads has gone away",
			func(in ScanReady, _ brine.Params, _ *brine.Recorder) (ScanReady, error) {
				if err := in.persist(); err != nil {
					return ScanReady{}, err
				}
				in.brokenConn = true
				return in, nil
			},
		),

		// The second enumeration alone. A closed connection cannot express
		// this: it would fail the first enumeration too, and the scanner would
		// never get as far as the one under test.
		brine.DefineMap[ScanReady, ScanReady](
			"the resource types table has been renamed out from under the scan",
			func(in ScanReady, _ brine.Params, _ *brine.Recorder) (ScanReady, error) {
				if err := in.persist(); err != nil {
					return ScanReady{}, err
				}
				if _, err := in.DB.Conn.Exec(
					`ALTER TABLE resource_types RENAME TO resource_types_hidden_by_the_scenario`); err != nil {
					return ScanReady{}, fmt.Errorf("hide the resource types table: %w", err)
				}
				in.typesTableRenamed = true
				return in, nil
			},
		),

		vanishingScopeStep(
			"the garbage collector deletes the scope before the scan can attach it",
			"attachment"),
		vanishingScopeStep(
			"the garbage collector deletes the scope before the scan can save the version",
			"version-save"),
	}
}

func vanishingScopeStep(pattern, moment string) brine.StepDefinition {
	return brine.DefineMap[ScanReady, ScanReady](pattern,
		func(in ScanReady, _ brine.Params, _ *brine.Recorder) (ScanReady, error) {
			in.scopeVanishes = moment
			return in, nil
		},
	)
}

func decodeVersion(raw string) (atc.Version, error) {
	var version atc.Version
	if err := json.Unmarshal([]byte(raw), &version); err != nil {
		return nil, fmt.Errorf("%q is not a JSON version: %w", raw, err)
	}
	return version, nil
}

// -----------------------------------------------------------------------
// When: the scan
// -----------------------------------------------------------------------

func scanRunDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ScanReady, ScanDone](
			"the scan runs",
			func(in ScanReady, _ brine.Params, _ *brine.Recorder) (ScanDone, error) {
				return in.run(ScanDone{Ready: in})
			},
		),

		// The retry. The fault is switched off first, which is what a
		// transient race IS: the collector has already been and gone, and the
		// next tick meets a database that works. Whether the resource resolves
		// now is the whole question — it only can if the failed pass left the
		// last-check time alone.
		brine.DefineMap[ScanDone, ScanDone](
			"the scan runs again",
			func(in ScanDone, _ brine.Params, _ *brine.Recorder) (ScanDone, error) {
				*in.Ready.faultActive = false
				return in.Ready.run(in)
			},
		),

		brine.DefineMap[ScanDone, ScanDone](
			"the registry now holds {string} at the digest {string}",
			func(in ScanDone, p brine.Params, _ *brine.Recorder) (ScanDone, error) {
				pattern := "the registry now holds {string} at the digest {string}"
				ref, digest, err := twoParams(pattern, p)
				if err != nil {
					return ScanDone{}, err
				}
				in.Ready.Registry.hold(ref, digest, "", "")
				return in, nil
			},
		),
	}
}

// run builds the scanner exactly as atccmd does and runs one pass, folding the
// result into whatever earlier passes left behind. The error is not returned
// as a step failure: whether the scan was refused is what the scenario goes on
// to assert.
func (r ScanReady) run(previous ScanDone) (ScanDone, error) {
	if err := r.persist(); err != nil {
		return ScanDone{}, err
	}

	// The scenario's one check factory, unless the point of the scenario is
	// that the database is gone — then it needs one over the closed
	// connection, and no scenario that does that also needs the in-flight set
	// the shared instance carries.
	checkFactory := r.CheckFactory
	if r.brokenConn {
		closed, err := r.DB.ClosedConn()
		if err != nil {
			return ScanDone{}, err
		}
		checkFactory = db.NewCheckFactory(
			closed, r.DB.LockFactory, r.CheckBuilds, util.NewSequenceGenerator(1))
	}

	configFactory := r.ConfigFactory
	if r.scopeVanishes != "" {
		configFactory = vanishingScopeFactory{
			ResourceConfigFactory: r.ConfigFactory,
			conn:                  r.DB.Conn,
			moment:                r.scopeVanishes,
			active:                r.faultActive,
		}
	}

	scanner := lidar.NewScanner(
		checkFactory,
		atc.NewPlanFactory(0),
		r.Workers,
		r.Resolver,
		configFactory,
	)

	// Zero the counter immediately before the pass, so the delta below belongs
	// to this pass alone.
	metric.Metrics.ChecksEnqueued.Delta()
	err := scanner.Run(lagerctx.NewContext(context.Background(), r.Logger))
	delta := int(metric.Metrics.ChecksEnqueued.Delta())

	// Run returns only once every scan worker has finished, so everything the
	// pass enqueued is already on the channel and a blocking read would only
	// be a way to hang.
	builds := drainChecks(r.CheckBuilds)
	for _, build := range builds {
		if finishErr := build.Finish(db.BuildStatusSucceeded); finishErr != nil {
			return ScanDone{}, fmt.Errorf("finish the check build for %q: %w", build.ResourceName(), finishErr)
		}
	}

	return ScanDone{
		Ready:  r,
		Err:    err,
		Builds: append(previous.Builds, builds...),
		Delta:  previous.Delta + delta,
	}, nil
}

func drainChecks(builds chan db.Build) []db.Build {
	var out []db.Build
	for {
		select {
		case build := <-builds:
			out = append(out, build)
		default:
			return out
		}
	}
}

// -----------------------------------------------------------------------
// The vanishing scope
// -----------------------------------------------------------------------

// vanishingScopeFactory reproduces the race the two FK guards in scanner.go
// exist for: the garbage collector removes a resource_config_scope while a
// scan is in the middle of using it.
//
// It injects TIMING, not an error. The row really is deleted, over the
// scenario's own connection, and what comes back is PostgreSQL's own 23503 on
// the real constraint — resources.resource_config_scope_id for the attachment
// moment, resource_config_versions.resource_config_scope_id for the save. The
// ginkgo suite hand-built a *pgconn.PgError instead, which a scanner
// classifying on error TEXT would have passed.
type vanishingScopeFactory struct {
	db.ResourceConfigFactory

	conn   db.DbConn
	moment string
	active *bool
}

func (f vanishingScopeFactory) FindOrCreateResourceConfig(
	resourceType string, source atc.Source, cache db.ResourceCache,
) (db.ResourceConfig, error) {
	config, err := f.ResourceConfigFactory.FindOrCreateResourceConfig(resourceType, source, cache)
	if err != nil {
		return nil, err
	}
	return vanishingScopeConfig{ResourceConfig: config, factory: f}, nil
}

type vanishingScopeConfig struct {
	db.ResourceConfig
	factory vanishingScopeFactory
}

func (c vanishingScopeConfig) FindOrCreateScope(resourceID *int) (db.ResourceConfigScope, error) {
	scope, err := c.ResourceConfig.FindOrCreateScope(resourceID)
	if err != nil {
		return nil, err
	}
	if !*c.factory.active {
		return scope, nil
	}
	if c.factory.moment == "attachment" {
		return scope, c.factory.deleteScope(scope.ID())
	}
	return vanishingScope{ResourceConfigScope: scope, factory: c.factory}, nil
}

type vanishingScope struct {
	db.ResourceConfigScope
	factory vanishingScopeFactory
}

func (s vanishingScope) SaveVersions(span db.SpanContext, versions []atc.Version) error {
	if *s.factory.active {
		if err := s.factory.deleteScope(s.ID()); err != nil {
			return err
		}
	}
	return s.ResourceConfigScope.SaveVersions(span, versions)
}

func (f vanishingScopeFactory) deleteScope(id int) error {
	if _, err := f.conn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, id); err != nil {
		return fmt.Errorf("collect the scope %d: %w", id, err)
	}
	return nil
}

// -----------------------------------------------------------------------
// Then: what the scan left behind
// -----------------------------------------------------------------------

func scanOutcomeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[ScanDone]("the scan completed without error",
			func(in ScanDone) error {
				if in.Err != nil {
					return fmt.Errorf("the scan failed: %v", in.Err)
				}
				return nil
			}),

		CheckContains[ScanDone]("the scan was refused, saying {string}",
			"the refusal",
			func(in ScanDone) (string, error) { return refusal(in.Err, "the scan") }),

		CheckMember[ScanDone]("a check was enqueued for {string}",
			"the resources a check was enqueued for",
			func(in ScanDone) ([]string, error) { return in.checkedResources(), nil }),

		CheckNotMember[ScanDone]("no check was enqueued for {string}",
			"the resources a check was enqueued for",
			func(in ScanDone) ([]string, error) { return in.checkedResources(), nil }),

		CheckCount[ScanDone]("{int} checks were enqueued",
			"checks",
			func(in ScanDone) ([]string, error) { return in.checkedResources(), nil }),

		// The set comparison, for the scenario about there being more
		// resources than scan workers. A count alone would pass on twenty
		// checks of the same resource.
		CheckThat[ScanDone]("every resource in the pipeline was checked exactly once",
			func(in ScanDone) error { return in.everyResourceCheckedOnce() }),

		CheckStringFor[ScanDone]("the resource {string} resolved to the digest {string}",
			"the digest on the resource",
			func(in ScanDone, name string) (string, error) { return in.resourceDigest(name) }),

		CheckStringFor[ScanDone]("the resource type {string} resolved to the digest {string}",
			"the digest on the resource type",
			func(in ScanDone, name string) (string, error) { return in.resourceTypeDigest(name) }),

		// "left unresolved" is the strong absence: the scan touched neither
		// the config nor the scope, so nothing about this resource changed at
		// all. Every scenario that uses it has a sibling that DID resolve,
		// because an absence passes on an empty table.
		CheckNotMember[ScanDone]("the resource {string} was left unresolved",
			"the resources the scan attached to a config",
			func(in ScanDone) ([]string, error) { return in.attachedResources() }),

		CheckNotMember[ScanDone]("the resource type {string} was left unresolved",
			"the resource types the scan attached to a config",
			func(in ScanDone) ([]string, error) { return in.attachedResourceTypes() }),

		// The weaker absence, and the one the interrupted-scan scenarios need:
		// the config may well have been attached before the failure, but no
		// version was ever recorded, so nothing downstream can use it.
		CheckNotMember[ScanDone]("the resource {string} holds no resolved digest",
			"the resources holding a resolved digest",
			func(in ScanDone) ([]string, error) { return in.resourcesWithDigests() }),

		CheckNotMember[ScanDone]("the resource type {string} holds no resolved digest",
			"the resource types holding a resolved digest",
			func(in ScanDone) ([]string, error) { return in.resourceTypesWithDigests() }),

		// What a step actually pulls. The digest is only useful once it is
		// joined back to the repository, and this is the joined form the image
		// fetch uses.
		CheckStringFor[ScanDone]("the resource type {string} will be pulled as {string}",
			"the image the resource type resolves to",
			func(in ScanDone, name string) (string, error) {
				resourceType, err := in.Ready.findResourceType(name)
				if err != nil {
					return "", err
				}
				return resourceType.ResolvedImage(), nil
			}),

		// The clock that decides whether the next tick bothers. A resolve that
		// did not record its end time re-resolves every tick; one that
		// recorded it after failing never resolves again.
		CheckMember[ScanDone]("the resource {string} recorded a successful check",
			"the resources with a successful check recorded",
			func(in ScanDone) ([]string, error) { return in.successfullyChecked() }),

		CheckInt[ScanDone]("the checks enqueued counter went up by {int}",
			"the checks enqueued counter",
			func(in ScanDone) (int, error) { return in.Delta, nil }),
	}
}

func (d ScanDone) checkedResources() []string {
	names := make([]string, 0, len(d.Builds))
	for _, build := range d.Builds {
		names = append(names, build.ResourceName())
	}
	sort.Strings(names)
	return names
}

func (d ScanDone) everyResourceCheckedOnce() error {
	var declared []string
	for _, draft := range d.Ready.Pipelines {
		for _, resource := range draft.Config.Resources {
			declared = append(declared, resource.Name)
		}
	}
	sort.Strings(declared)
	checked := d.checkedResources()
	if strings.Join(declared, ",") != strings.Join(checked, ",") {
		return fmt.Errorf("expected every resource in the pipeline to be checked exactly once;\n"+
			"  the pipeline holds %v\n  and checks were enqueued for %v", declared, checked)
	}
	return nil
}

func (d ScanDone) resourceDigest(name string) (string, error) {
	resource, err := d.Ready.findResource(name)
	if err != nil {
		return "", err
	}
	if resource.ResourceConfigScopeID() == 0 {
		return "", fmt.Errorf("the resource %q was never attached to a config scope, "+
			"so the scan resolved nothing for it", name)
	}
	return d.latestDigest(resource.ResourceConfigScopeID())
}

func (d ScanDone) resourceTypeDigest(name string) (string, error) {
	resourceType, err := d.Ready.findResourceType(name)
	if err != nil {
		return "", err
	}
	if resourceType.ResourceConfigScopeID() == 0 {
		return "", fmt.Errorf("the resource type %q was never attached to a config scope, "+
			"so the scan resolved nothing for it", name)
	}
	return d.latestDigest(resourceType.ResourceConfigScopeID())
}

// latestDigest reads the version row itself rather than going back through
// FindOrCreateScope, which would CREATE a scope that the scan had failed to
// leave behind and quietly turn an absence into a pass.
func (d ScanDone) latestDigest(scopeID int) (string, error) {
	var raw string
	err := d.Ready.DB.Conn.QueryRow(`
		SELECT version FROM resource_config_versions
		WHERE resource_config_scope_id = $1
		ORDER BY check_order DESC, id DESC LIMIT 1`, scopeID).Scan(&raw)
	if err != nil {
		return "", fmt.Errorf("read the version on scope %d: %w", scopeID, err)
	}
	version, err := decodeVersion(raw)
	if err != nil {
		return "", err
	}
	digest, ok := version["digest"]
	if !ok {
		return "", fmt.Errorf("the version on scope %d is %s, which carries no digest", scopeID, raw)
	}
	return digest, nil
}

func (d ScanDone) attachedResources() ([]string, error) {
	return d.names(`
		SELECT name FROM resources
		WHERE resource_config_id IS NOT NULL OR resource_config_scope_id IS NOT NULL
		ORDER BY name`)
}

func (d ScanDone) attachedResourceTypes() ([]string, error) {
	return d.names(`SELECT name FROM resource_types WHERE resource_config_id IS NOT NULL ORDER BY name`)
}

func (d ScanDone) resourcesWithDigests() ([]string, error) {
	return d.names(`
		SELECT DISTINCT r.name FROM resources r
		JOIN resource_config_versions v ON v.resource_config_scope_id = r.resource_config_scope_id
		WHERE v.version ? 'digest'
		ORDER BY r.name`)
}

func (d ScanDone) resourceTypesWithDigests() ([]string, error) {
	return d.names(`
		SELECT DISTINCT t.name FROM resource_types t
		JOIN resource_config_scopes s ON s.resource_config_id = t.resource_config_id AND s.resource_id IS NULL
		JOIN resource_config_versions v ON v.resource_config_scope_id = s.id
		WHERE v.version ? 'digest'
		ORDER BY t.name`)
}

func (d ScanDone) successfullyChecked() ([]string, error) {
	return d.names(`
		SELECT r.name FROM resources r
		JOIN resource_config_scopes s ON s.id = r.resource_config_scope_id
		WHERE s.last_check_succeeded AND s.last_check_end_time > '1970-01-02'
		ORDER BY r.name`)
}

func (d ScanDone) names(query string) ([]string, error) {
	rows, err := d.Ready.DB.Conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("read the rows the scan left behind: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read a name: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------
// Then: the check plan
// -----------------------------------------------------------------------

// The plan is not decoration and it is not a call record: it is the document
// the check build executes. Every field below decides something a check pod
// then does — which registry it talks to, which worker tags it will land on,
// how long it is allowed to take, and, for a resource sitting on a custom
// type, whether that type's own image is checked and fetched first.
//
// These assertions run through lidar but they pin atc/db's CheckPlan and
// atc/builds' planner, which is where they belong; what is lidar's own is
// WHICH checkable and WHICH pipeline's resource types reach them, and the
// parent-type half below is the only observable proof of the second.

func checkPlanDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		planCheck("the check plan for {string} checks the resource {string}",
			"the resource the check plan names",
			func(plan atc.CheckPlan) string { return plan.Resource }),

		planCheck("the check plan for {string} runs the type {string}",
			"the type the check plan runs",
			func(plan atc.CheckPlan) string { return plan.Type }),

		planCheck("the check plan for {string} has the source {string}",
			"the source in the check plan",
			func(plan atc.CheckPlan) string { return renderSource(plan.Source) }),

		planCheck("the check plan for {string} carries the tags {string}",
			"the tags in the check plan",
			func(plan atc.CheckPlan) string { return strings.Join(plan.Tags, ",") }),

		planCheck("the check plan for {string} times out after {string}",
			"the timeout in the check plan",
			func(plan atc.CheckPlan) string { return plan.Timeout }),

		planCheck("the check plan for {string} repeats every {string}",
			"the interval in the check plan",
			func(plan atc.CheckPlan) string { return plan.Interval.Interval.String() }),

		// "nothing" rather than an empty string, because a pin that vanished
		// and a pin that was never set have to look different on the page.
		planCheck("the check plan for {string} starts from the version {string}",
			"the version the check plan starts from",
			func(plan atc.CheckPlan) string { return renderVersion(plan.FromVersion) }),

		planCheck("the check plan for {string} pulls its image from the base type {string}",
			"the base type in the check plan",
			func(plan atc.CheckPlan) string { return plan.TypeImage.BaseType }),

		nestedPlanCheck("the parent type check in the plan for {string} names {string}",
			"the parent type check",
			func(image atc.TypeImage) *atc.Plan { return image.CheckPlan },
			func(plan atc.CheckPlan) string { return plan.Name }),

		nestedPlanCheck("the parent type check in the plan for {string} has the source {string}",
			"the parent type check",
			func(image atc.TypeImage) *atc.Plan { return image.CheckPlan },
			func(plan atc.CheckPlan) string { return renderSource(plan.Source) }),

		nestedPlanCheck("the parent type check in the plan for {string} carries the tags {string}",
			"the parent type check",
			func(image atc.TypeImage) *atc.Plan { return image.CheckPlan },
			func(plan atc.CheckPlan) string { return strings.Join(plan.Tags, ",") }),

		nestedGetCheck("the parent type fetch in the plan for {string} names {string}",
			func(plan atc.GetPlan) string { return plan.Name }),

		nestedGetCheck("the parent type fetch in the plan for {string} runs the type {string}",
			func(plan atc.GetPlan) string { return plan.Type }),

		nestedGetCheck("the parent type fetch in the plan for {string} has the source {string}",
			func(plan atc.GetPlan) string { return renderSource(plan.Source) }),
	}
}

func planCheck(pattern, subject string, read func(atc.CheckPlan) string) brine.StepDefinition {
	return CheckStringFor[ScanDone](pattern, subject,
		func(in ScanDone, name string) (string, error) {
			plan, err := in.checkPlan(name)
			if err != nil {
				return "", err
			}
			return read(plan), nil
		})
}

func nestedPlanCheck(pattern, subject string, pick func(atc.TypeImage) *atc.Plan, read func(atc.CheckPlan) string) brine.StepDefinition {
	return CheckStringFor[ScanDone](pattern, subject,
		func(in ScanDone, name string) (string, error) {
			plan, err := in.checkPlan(name)
			if err != nil {
				return "", err
			}
			nested := pick(plan.TypeImage)
			if nested == nil || nested.Check == nil {
				return "", fmt.Errorf("the check plan for %q carries no %s, so the custom type it "+
					"depends on would never be checked before the resource itself is", name, subject)
			}
			return read(*nested.Check), nil
		})
}

func nestedGetCheck(pattern string, read func(atc.GetPlan) string) brine.StepDefinition {
	return CheckStringFor[ScanDone](pattern, "the parent type fetch",
		func(in ScanDone, name string) (string, error) {
			plan, err := in.checkPlan(name)
			if err != nil {
				return "", err
			}
			nested := plan.TypeImage.GetPlan
			if nested == nil || nested.Get == nil {
				return "", fmt.Errorf("the check plan for %q carries no parent type fetch, so the "+
					"check pod would have no image to run", name)
			}
			return read(*nested.Get), nil
		})
}

func (d ScanDone) checkPlan(name string) (atc.CheckPlan, error) {
	for _, build := range d.Builds {
		if build.ResourceName() != name {
			continue
		}
		plan := build.PrivatePlan()
		if plan.Check == nil {
			return atc.CheckPlan{}, fmt.Errorf("the build enqueued for %q is not a check", name)
		}
		return *plan.Check, nil
	}
	return atc.CheckPlan{}, fmt.Errorf("no check was enqueued for %q; checks were enqueued for %v",
		name, d.checkedResources())
}

// renderSource and renderVersion spell a map as "key: value, key: value",
// sorted, rather than as JSON.
//
// That is not a stylistic choice. brine compiles {string} to "([^"]*)", so a
// parameter cannot contain a double quote, and JSON is nothing but double
// quotes. A scenario writing a source as JSON does not fail at the assertion —
// the step line does not match any definition at all, which the vocabulary
// guards catch but a reader of the feature file would not.
func renderSource(source atc.Source) string {
	pairs := make(map[string]any, len(source))
	for k, v := range source {
		pairs[k] = v
	}
	return renderPairs(pairs)
}

func renderVersion(version atc.Version) string {
	pairs := make(map[string]any, len(version))
	for k, v := range version {
		pairs[k] = v
	}
	return renderPairs(pairs)
}

func renderPairs(pairs map[string]any) string {
	if len(pairs) == 0 {
		return "nothing"
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rendered := make([]string, 0, len(keys))
	for _, k := range keys {
		rendered = append(rendered, fmt.Sprintf("%s: %v", k, pairs[k]))
	}
	return strings.Join(rendered, ", ")
}

// parseVersionText reads the same spelling back, so the version a scenario
// pins is written the way the version a plan carries is read.
func parseVersionText(raw string) (atc.Version, error) {
	version := atc.Version{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("%q is not a version: expected \"key: value\" pairs", raw)
		}
		version[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return version, nil
}
