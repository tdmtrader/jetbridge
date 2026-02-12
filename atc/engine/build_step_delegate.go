package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/yaml"
)

type buildStepDelegate struct {
	build                db.Build
	planID               atc.PlanID
	clock                clock.Clock
	state                exec.RunState
	stderr               io.Writer
	stdout               io.Writer
	policyChecker        policy.Checker
	disableRedactSecrets bool
	nativeImageFetch     bool

	// Optional factories for metadata-only FetchImage on K8s.
	// When set and nativeImageFetch is true, FetchImage resolves custom type
	// images from cached DB versions instead of spawning check+get pods.
	resourceConfigFactory db.ResourceConfigFactory
	resourceCacheFactory  db.ResourceCacheFactory
}

func NewBuildStepDelegate(
	build db.Build,
	planID atc.PlanID,
	state exec.RunState,
	clock clock.Clock,
	policyChecker policy.Checker,
	disableRedactSecrets bool,
	nativeImageFetch bool,
) *buildStepDelegate {
	return &buildStepDelegate{
		build:                build,
		planID:               planID,
		clock:                clock,
		state:                state,
		stdout:               nil,
		stderr:               nil,
		policyChecker:        policyChecker,
		disableRedactSecrets: disableRedactSecrets,
		nativeImageFetch:     nativeImageFetch,
	}
}

// NewBuildStepDelegateWithFactories creates a BuildStepDelegate with resource
// factories configured for metadata-only FetchImage on K8s.
func NewBuildStepDelegateWithFactories(
	build db.Build,
	planID atc.PlanID,
	state exec.RunState,
	clock clock.Clock,
	policyChecker policy.Checker,
	disableRedactSecrets bool,
	nativeImageFetch bool,
	resourceConfigFactory db.ResourceConfigFactory,
	resourceCacheFactory db.ResourceCacheFactory,
) exec.BuildStepDelegate {
	d := NewBuildStepDelegate(build, planID, state, clock, policyChecker, disableRedactSecrets, nativeImageFetch)
	d.resourceConfigFactory = resourceConfigFactory
	d.resourceCacheFactory = resourceCacheFactory
	return d
}

func (delegate *buildStepDelegate) StartSpan(
	ctx context.Context,
	component string,
	extraAttrs tracing.Attrs,
) (context.Context, trace.Span) {
	attrs := delegate.build.TracingAttrs()
	maps.Copy(attrs, extraAttrs)

	return tracing.StartSpan(ctx, component, attrs)
}

func (delegate *buildStepDelegate) Stdout() io.Writer {
	if delegate.stdout != nil {
		return delegate.stdout
	}
	delegate.stdout = newDBEventWriter(
		delegate.build,
		event.Origin{
			Source: event.OriginSourceStdout,
			ID:     event.OriginID(delegate.planID),
		},
		delegate.clock,
		delegate.buildOutputFilter,
		delegate.disableRedactSecrets,
	)
	return delegate.stdout
}

func (delegate *buildStepDelegate) Stderr() io.Writer {
	if delegate.stderr != nil {
		return delegate.stderr
	}
	delegate.stderr = newDBEventWriter(
		delegate.build,
		event.Origin{
			Source: event.OriginSourceStderr,
			ID:     event.OriginID(delegate.planID),
		},
		delegate.clock,
		delegate.buildOutputFilter,
		delegate.disableRedactSecrets,
	)
	return delegate.stderr
}

func (delegate *buildStepDelegate) Initializing(logger lager.Logger) {
	err := delegate.build.SaveEvent(event.Initialize{
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Time: time.Now().Unix(),
	})
	if err != nil {
		logger.Error("failed-to-save-initialize-event", err)
		return
	}

	logger.Info("initializing")
}

func (delegate *buildStepDelegate) Starting(logger lager.Logger) {
	err := delegate.build.SaveEvent(event.Start{
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Time: time.Now().Unix(),
	})
	if err != nil {
		logger.Error("failed-to-save-start-event", err)
		return
	}

	logger.Debug("starting")
}

func (delegate *buildStepDelegate) Finished(logger lager.Logger, succeeded bool) {
	// PR#4398: close to flush stdout and stderr
	delegate.Stdout().(io.Closer).Close()
	delegate.Stderr().(io.Closer).Close()

	err := delegate.build.SaveEvent(event.Finish{
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Time:      time.Now().Unix(),
		Succeeded: succeeded,
	})
	if err != nil {
		logger.Error("failed-to-save-finish-event", err)
		return
	}

	logger.Info("finished")
}

func (delegate *buildStepDelegate) BuildStartTime() time.Time {
	return delegate.build.StartTime()
}

func (delegate *buildStepDelegate) BeforeSelectWorker(logger lager.Logger) error {
	if delegate.build.ResourceID() != 0 {
		// For check builds, once a check build needs to select a worker, then
		// we consider it needs to do run a real check. If it runs a real check,
		// then we want to log it, so we call OnCheckBuildStart here to ensure
		// the build can be logged. Note that, OnCheckBuildStart can be safely
		// called multiple times.
		err := delegate.build.OnCheckBuildStart()
		if err != nil {
			logger.Error("failed-to-call-on-check-build-start", err)
			return err
		}
	}
	return nil
}

func (delegate *buildStepDelegate) WaitingForWorker(logger lager.Logger) {
	err := delegate.build.SaveEvent(event.WaitingForWorker{
		Time: time.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
	})
	if err != nil {
		logger.Error("failed-to-save-waiting-for-worker-event", err)
		return
	}
}

func (delegate *buildStepDelegate) SelectedWorker(logger lager.Logger, worker string) {
	err := delegate.build.SaveEvent(event.SelectedWorker{
		Time: time.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		WorkerName: worker,
	})

	if err != nil {
		logger.Error("failed-to-save-selected-worker-event", err)
		return
	}
}

func (delegate *buildStepDelegate) StreamingVolume(logger lager.Logger, volume string, sourceWorker string, destWorker string) {
	err := delegate.build.SaveEvent(event.StreamingVolume{
		Time: time.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Volume:       volume,
		SourceWorker: sourceWorker,
		DestWorker:   destWorker,
	})

	if err != nil {
		logger.Error("failed-to-save-streaming-volume-event", err)
		return
	}
}

func (delegate *buildStepDelegate) WaitingForStreamedVolume(logger lager.Logger, volume string, destWorker string) {
	err := delegate.build.SaveEvent(event.WaitingForStreamedVolume{
		Time: time.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Volume:     volume,
		DestWorker: destWorker,
	})

	if err != nil {
		logger.Error("failed-to-save-waiting-for-streamed-volume-event", err)
		return
	}
}

func (delegate *buildStepDelegate) Errored(logger lager.Logger, message string) {
	err := delegate.build.SaveEvent(event.Error{
		Message: message,
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Time: delegate.clock.Now().Unix(),
	})
	if err != nil {
		logger.Error("failed-to-save-error-event", err)
	}
}

func (delegate *buildStepDelegate) FetchImage(
	ctx context.Context,
	getPlan atc.Plan,
	checkPlan *atc.Plan,
	privileged bool,
) (runtime.ImageSpec, db.ResourceCache, error) {
	err := delegate.checkImagePolicy(getPlan.Get.Source, getPlan.Get.Type, privileged)
	if err != nil {
		return runtime.ImageSpec{}, nil, err
	}

	// Try metadata-only path when on K8s with resource factories available.
	// This resolves type images from cached DB versions (populated by lidar)
	// instead of spawning check+get pods.
	if delegate.nativeImageFetch && delegate.resourceConfigFactory != nil && delegate.resourceCacheFactory != nil {
		spec, cache, err := delegate.metadataFetchImage(ctx, getPlan, privileged)
		if err == nil {
			return spec, cache, nil
		}
		// Fall through to plan-based fetch on any error (no cached version, etc.)
	}

	fetchState := delegate.state.NewLocalScope()

	if checkPlan != nil {
		ok, err := fetchState.Run(ctx, *checkPlan)
		if err != nil {
			return runtime.ImageSpec{}, nil, err
		}

		if !ok {
			return runtime.ImageSpec{}, nil, fmt.Errorf("image check failed")
		}
	}

	ok, err := fetchState.Run(ctx, getPlan)
	if err != nil {
		return runtime.ImageSpec{}, nil, err
	}

	if !ok {
		return runtime.ImageSpec{}, nil, fmt.Errorf("image fetching failed")
	}

	var result exec.GetResult
	if !fetchState.Result(getPlan.ID, &result) {
		return runtime.ImageSpec{}, nil, fmt.Errorf("get did not return a result")
	}

	if result.ResourceCache != nil {
		err = delegate.build.SaveImageResourceVersion(result.ResourceCache)
		if err != nil {
			return runtime.ImageSpec{}, nil, fmt.Errorf("save image version: %w", err)
		}
	}

	artifact, _, found := fetchState.ArtifactRepository().ArtifactFor(build.ArtifactName(result.Name))
	if !found {
		return runtime.ImageSpec{}, nil, fmt.Errorf("fetched artifact not found")
	}

	var version atc.Version
	if result.ResourceCache != nil {
		version = result.ResourceCache.Version()
	}
	imageURL := imageURLFromSource(getPlan.Get.Type, getPlan.Get.Produces, getPlan.Get.Source, version)

	return runtime.ImageSpec{
		ImageArtifact: artifact,
		ImageURL:      imageURL,
		Privileged:    privileged,
	}, result.ResourceCache, nil
}

// imageURLFromSource constructs a Docker image URL from a resource type's
// source config and fetched version. This is used by the K8s runtime which
// needs an image reference string rather than an artifact volume. The Garden
// runtime ignores ImageURL when ImageArtifact is set, so setting both is safe.
//
// The produces parameter allows custom types that produce registry-compatible
// images to also get a URL constructed (e.g. a type with produces: registry-image).
func imageURLFromSource(resourceType string, produces string, source atc.Source, version atc.Version) string {
	if resourceType != "registry-image" && produces != "registry-image" {
		return ""
	}

	repo, ok := source["repository"].(string)
	if !ok || repo == "" {
		return ""
	}

	ref := repo

	if digest, ok := version["digest"]; ok && digest != "" {
		ref += "@" + digest
	} else if tag, ok := source["tag"].(string); ok && tag != "" {
		ref += ":" + tag
	}

	return "docker:///" + ref
}

// metadataFetchImage resolves a type image from cached DB versions without
// running check+get plans. This is used on K8s where kubelet handles image
// pulls natively and we only need the image reference URL.
//
// It looks up the latest version for the image's resource config (populated
// by lidar's periodic checks) and constructs an ImageURL + ResourceCache.
// Returns an error if the metadata path can't be used (no cached version,
// unsupported type, etc.), signaling the caller to fall back to plan-based fetch.
func (delegate *buildStepDelegate) metadataFetchImage(
	ctx context.Context,
	getPlan atc.Plan,
	privileged bool,
) (runtime.ImageSpec, db.ResourceCache, error) {
	// Only types that are or produce registry-image support metadata-only
	// resolution. The source must follow registry-image conventions (repository field).
	isRegistryImage := getPlan.Get.Type == "registry-image" || getPlan.Get.Produces == "registry-image"
	if !isRegistryImage {
		return runtime.ImageSpec{}, nil, fmt.Errorf("metadata-only fetch not supported for type %q", getPlan.Get.Type)
	}

	// Evaluate source credentials
	source, err := creds.NewSource(delegate.state, getPlan.Get.Source).Evaluate()
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("evaluate source: %w", err)
	}

	// Find the resource config for this type+source (same config lidar uses)
	resourceConfig, err := delegate.resourceConfigFactory.FindOrCreateResourceConfig(
		getPlan.Get.Type,
		source,
		nil, // base type, no parent cache
	)
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("find resource config: %w", err)
	}

	// Find scope (nil resource ID - type images aren't scoped to a pipeline resource)
	scope, err := resourceConfig.FindOrCreateScope(nil)
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("find scope: %w", err)
	}

	// Get latest version from DB (populated by lidar's periodic checks)
	latestVersion, found, err := scope.LatestVersion()
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("get latest version: %w", err)
	}
	if !found {
		return runtime.ImageSpec{}, nil, fmt.Errorf("no cached version for type %q", getPlan.Get.Type)
	}

	version := atc.Version(latestVersion.Version())

	// Construct image URL (e.g. docker:///repo@sha256:abc123)
	imageURL := imageURLFromSource(getPlan.Get.Type, getPlan.Get.Produces, source, version)
	if imageURL == "" {
		return runtime.ImageSpec{}, nil, fmt.Errorf("cannot construct image URL for type %q", getPlan.Get.Type)
	}

	// Evaluate params for resource cache key
	params, err := creds.NewParams(delegate.state, getPlan.Get.Params).Evaluate()
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("evaluate params: %w", err)
	}

	// Create/find resource cache for the type image version.
	// This cache is needed by calling steps (check_step, get_step, etc.)
	// to build the resource config chain for custom resource types.
	resourceCache, err := delegate.resourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(delegate.build.ID()),
		getPlan.Get.Type,
		version,
		source,
		params,
		nil, // base type
	)
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("create resource cache: %w", err)
	}

	// Save image resource version for build tracking
	err = delegate.build.SaveImageResourceVersion(resourceCache)
	if err != nil {
		return runtime.ImageSpec{}, nil, fmt.Errorf("save image version: %w", err)
	}

	return runtime.ImageSpec{
		ImageURL:   imageURL,
		Privileged: privileged,
	}, resourceCache, nil
}

func (delegate *buildStepDelegate) ConstructAcrossSubsteps(templateBytes []byte, acrossVars []atc.AcrossVar, valueCombinations [][]any) ([]atc.VarScopedPlan, error) {
	template := vars.NewTemplate(templateBytes)
	substeps := make([]atc.VarScopedPlan, len(valueCombinations))
	substepsPublic := make([]*json.RawMessage, len(substeps))
	for i, values := range valueCombinations {
		localVars := vars.StaticVariables{}
		for j, v := range acrossVars {
			localVars[v.Var] = values[j]
		}
		interpolatedBytes, err := template.Evaluate(vars.NamedVariables{".": localVars}, vars.EvaluateOpts{})
		if err != nil {
			return nil, fmt.Errorf("failed to interpolate template: %w", err)
		}
		var subPlan atc.Plan
		// This must use sigs.k8s.io/yaml, since gopkg.in/yaml.v2 doesn't
		// convert from YAML -> JSON first.
		if err := yaml.Unmarshal(interpolatedBytes, &subPlan); err != nil {
			return nil, fmt.Errorf("invalid template bytes: %w", err)
		}

		// Maps from the original subplan ID generated by the planner to the
		// translated ID unique to the substep iteration.
		mappedSubplanIDs := map[atc.PlanID]atc.PlanID{}
		planIDCounter := 0
		subPlan.Each(func(p *atc.Plan) {
			mappedID := atc.PlanID(fmt.Sprintf("%s/%d/%d", delegate.planID, i, planIDCounter))
			mappedSubplanIDs[p.ID] = mappedID
			p.ID = mappedID
			planIDCounter++
		})

		subPlan.Each(func(p *atc.Plan) {
			// Ensure VersionFrom is mapped to the correct subplan within the
			// substep. Note that the VersionFrom plan ID can theoretically
			// reside outside of the substep, in which case no mapping is
			// necessary.
			if p.Get != nil && p.Get.VersionFrom != nil {
				if mappedID, ok := mappedSubplanIDs[*p.Get.VersionFrom]; ok {
					p.Get.VersionFrom = &mappedID
				}
			}
		})
		substeps[i] = atc.VarScopedPlan{
			Step:   subPlan,
			Values: values,
		}
		substepsPublic[i] = substeps[i].Public()
	}

	err := delegate.build.SaveEvent(event.AcrossSubsteps{
		Time: delegate.clock.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(delegate.planID),
		},
		Substeps: substepsPublic,
	})
	if err != nil {
		return nil, fmt.Errorf("save across substeps event: %w", err)
	}

	return substeps, nil
}

func (delegate *buildStepDelegate) checkImagePolicy(imageSource atc.Source, imageType string, privileged bool) error {
	if !delegate.policyChecker.ShouldCheckAction(policy.ActionUseImage) {
		return nil
	}

	redactedSource, err := delegate.redactImageSource(imageSource)
	if err != nil {
		return fmt.Errorf("redact source: %w", err)
	}

	return delegate.checkPolicy(policy.PolicyCheckInput{
		Action:   policy.ActionUseImage,
		Team:     delegate.build.TeamName(),
		Pipeline: delegate.build.PipelineName(),
		Data: map[string]any{
			"image_type":   imageType,
			"image_source": redactedSource,
			"privileged":   privileged,
		},
	})
}

func (delegate *buildStepDelegate) checkPolicy(input policy.PolicyCheckInput) error {
	result, err := delegate.policyChecker.Check(input)
	if err != nil {
		return fmt.Errorf("policy check: %w", err)
	}

	if !result.Allowed() {
		policyCheckErr := policy.PolicyCheckNotPass{
			Messages: result.Messages(),
		}
		if result.ShouldBlock() {
			return policyCheckErr
		} else {
			fmt.Fprintf(delegate.Stderr(), "\x1b[1;33m%s\x1b[0m\n\n", policyCheckErr.Error())
			fmt.Fprintln(delegate.Stderr(), "\x1b[33mWARNING: unblocking from the policy check failure for soft enforcement\x1b[0m")
		}
	}

	return nil
}

func (delegate *buildStepDelegate) buildOutputFilter(str string) string {
	it := &credVarsIterator{line: str}
	delegate.state.IterateInterpolatedCreds(it)
	return it.line
}

func (delegate *buildStepDelegate) redactImageSource(source atc.Source) (atc.Source, error) {
	b, err := json.Marshal(&source)
	if err != nil {
		return source, err
	}
	s := delegate.buildOutputFilter(string(b))
	newSource := atc.Source{}
	err = json.Unmarshal([]byte(s), &newSource)
	if err != nil {
		return source, err
	}
	return newSource, nil
}

func (delegate *buildStepDelegate) ContainerOwner(planId atc.PlanID) db.ContainerOwner {
	return delegate.build.ContainerOwner(planId)
}

type credVarsIterator struct {
	line string
}

func (it *credVarsIterator) YieldCred(name, value string) {
	for lineValue := range strings.SplitSeq(value, "\n") {
		lineValue = strings.TrimSpace(lineValue)
		// Don't consider a single char as a secret.
		if len(lineValue) > 1 {
			it.line = strings.ReplaceAll(it.line, lineValue, "((redacted))")
		}
	}
}
