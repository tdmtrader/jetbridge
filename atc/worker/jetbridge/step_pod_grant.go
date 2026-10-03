package jetbridge

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"regexp"
	"strings"

	"github.com/concourse/concourse/atc/db"
	"k8s.io/apimachinery/pkg/util/validation"
)

// A StepPodGrant maps a build's authoritative owner to the ServiceAccount its
// step pods run under. Grants come only from web's configuration
// (--kubernetes-step-pod-grant); nothing a pipeline, task, resource type, API
// caller or fly user supplies can name one or select a ServiceAccount.
type StepPodGrant struct {
	Name  string
	Owner StepPodOwner
	// ServiceAccount is empty for a grant that only allows privilege: its
	// pods run as the default step pod identity's account, with no token.
	ServiceAccount string
	// Privileged allows the owner's step pods to run privileged
	// (step_pod_privilege_gate).
	Privileged bool
}

// A StepPodOwner is who a build belongs to, as far as grants are concerned:
// an exact team/pipeline/job of a non-instanced pipeline, or a one-off build
// of team main. Every other build -- a check, an instanced pipeline's job, a
// one-off of another team -- has no owner and matches no grant.
type StepPodOwner struct {
	Team     string
	Pipeline string
	Job      string
	OneOff   bool
}

// oneOffTeam is the only team whose one-off builds can hold a grant.
const oneOffTeam = "main"

func (owner StepPodOwner) String() string {
	if owner.OneOff {
		return owner.Team + "/one-off"
	}
	return owner.Team + "/" + owner.Pipeline + "/" + owner.Job
}

// StepPodBuilds looks a build up by the id the engine put in its container
// metadata. db.BuildFactory satisfies it.
type StepPodBuilds interface {
	Build(int) (db.Build, bool, error)
}

// stepPodIdentity is what a step pod runs as. grant is empty for the default
// step pod identity; ownAccount is false when the pod runs as the default
// account, whether or not a grant allows it privilege.
type stepPodIdentity struct {
	grant          string
	serviceAccount string
	ownAccount     bool
	privileged     bool
}

// identifierSegment is a team, pipeline or job name as atc.ValidateIdentifier
// accepts it.
var identifierSegment = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]*$`)

// ParseStepPodGrant parses one --kubernetes-step-pod-grant value:
//
//	name=<grant>,owner=<team>/<pipeline>/<job>|main/one-off[,service-account=<sa>][,privileged=true|false]
//
// A grant names a ServiceAccount, allows privilege, or both.
func ParseStepPodGrant(value string) (StepPodGrant, error) {
	var grant StepPodGrant
	seen := map[string]bool{}
	var owner string
	for _, field := range strings.Split(value, ",") {
		key, val, ok := strings.Cut(field, "=")
		if !ok {
			return StepPodGrant{}, fmt.Errorf("step pod grant %q: %q is not key=value", value, field)
		}
		if seen[key] {
			return StepPodGrant{}, fmt.Errorf("step pod grant %q: %s given twice", value, key)
		}
		seen[key] = true
		switch key {
		case "name":
			grant.Name = val
		case "owner":
			owner = val
		case "service-account":
			grant.ServiceAccount = val
		case "privileged":
			switch val {
			case "true":
				grant.Privileged = true
			case "false":
			default:
				return StepPodGrant{}, fmt.Errorf("step pod grant %q: privileged must be true or false, not %q", value, val)
			}
		default:
			return StepPodGrant{}, fmt.Errorf("step pod grant %q: unknown key %q", value, key)
		}
	}

	if !identifierSegment.MatchString(grant.Name) {
		return StepPodGrant{}, fmt.Errorf("step pod grant %q: name %q is not an identifier", value, grant.Name)
	}
	parsedOwner, err := parseStepPodOwner(owner)
	if err != nil {
		return StepPodGrant{}, fmt.Errorf("step pod grant %q: %w", value, err)
	}
	grant.Owner = parsedOwner
	if grant.ServiceAccount == "" && !grant.Privileged {
		return StepPodGrant{}, fmt.Errorf("step pod grant %q: a grant needs a service-account, privileged=true, or both", value)
	}
	if grant.ServiceAccount != "" {
		if errs := validation.IsDNS1123Subdomain(grant.ServiceAccount); len(errs) > 0 {
			return StepPodGrant{}, fmt.Errorf("step pod grant %q: service-account %q: %s", value, grant.ServiceAccount, strings.Join(errs, "; "))
		}
	}

	return grant, nil
}

func parseStepPodOwner(owner string) (StepPodOwner, error) {
	segments := strings.Split(owner, "/")
	for _, segment := range segments {
		if !identifierSegment.MatchString(segment) {
			return StepPodOwner{}, fmt.Errorf("owner %q: %q is not an identifier", owner, segment)
		}
	}
	switch {
	case len(segments) == 2 && segments[1] == "one-off":
		if segments[0] != oneOffTeam {
			return StepPodOwner{}, fmt.Errorf("owner %q: only team %s's one-off builds can hold a grant", owner, oneOffTeam)
		}
		return StepPodOwner{Team: segments[0], OneOff: true}, nil
	case len(segments) == 3:
		return StepPodOwner{Team: segments[0], Pipeline: segments[1], Job: segments[2]}, nil
	default:
		return StepPodOwner{}, fmt.Errorf("owner %q: want <team>/<pipeline>/<job> or %s/one-off", owner, oneOffTeam)
	}
}

// ValidateStepPodGrants refuses two grants with one name or one owner: either
// would make which identity a build gets depend on flag order.
func ValidateStepPodGrants(grants []StepPodGrant) error {
	names := map[string]bool{}
	owners := map[StepPodOwner]string{}
	for _, grant := range grants {
		if names[grant.Name] {
			return fmt.Errorf("step pod grant %q is configured twice", grant.Name)
		}
		names[grant.Name] = true
		if other, ok := owners[grant.Owner]; ok {
			return fmt.Errorf("step pod grants %q and %q both name owner %s", other, grant.Name, grant.Owner)
		}
		owners[grant.Owner] = grant.Name
	}
	return nil
}

// stepPodOwnerOf derives a build's owner from its row, and only its row. A
// check build, an instanced or Run pipeline's build, and another team's
// one-off have none.
func stepPodOwnerOf(build db.Build) (StepPodOwner, bool) {
	if build.Name() == db.CheckBuildName || build.ResourceID() != 0 || build.ResourceTypeID() != 0 {
		return StepPodOwner{}, false
	}
	if build.PipelineID() != 0 {
		if len(build.PipelineInstanceVars()) != 0 {
			return StepPodOwner{}, false
		}
		if _, ok := build.PipelineRunID(); ok {
			return StepPodOwner{}, false
		}
	}
	if build.JobID() != 0 {
		return StepPodOwner{Team: build.TeamName(), Pipeline: build.PipelineName(), Job: build.JobName()}, true
	}
	if build.TeamName() != oneOffTeam {
		return StepPodOwner{}, false
	}
	return StepPodOwner{Team: build.TeamName(), OneOff: true}, true
}

// stepPodIdentities resolves the identity of a container's pods. It is built
// once per worker from web's configuration.
type stepPodIdentities struct {
	grants         []StepPodGrant
	builds         StepPodBuilds
	defaultAccount string
}

// describe names the identity in a refusal.
func (identity stepPodIdentity) describe() string {
	if identity.grant == "" {
		return fmt.Sprintf("the default step pod identity (ServiceAccount %q, no API token)", podAccount(identity.serviceAccount))
	}
	if !identity.ownAccount {
		return fmt.Sprintf("grant %q (the default ServiceAccount %q, no API token)", identity.grant, podAccount(identity.serviceAccount))
	}
	return fmt.Sprintf("grant %q (ServiceAccount %q)", identity.grant, identity.serviceAccount)
}

// identityConforms says whether an existing pod already runs as identity: the
// same ServiceAccount, and a token only where the identity has its own
// account. An unset token setting means mounted, which is what a Release A
// pod of a granted job carries, so it still conforms.
func identityConforms(pod *corev1.Pod, identity stepPodIdentity) bool {
	if podAccount(pod.Spec.ServiceAccountName) != podAccount(identity.serviceAccount) {
		return false
	}
	mounted := pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken
	return mounted == identity.ownAccount
}

// podAccount is the ServiceAccount a pod spec runs as; an empty name is the
// namespace's default.
func podAccount(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func tokenState(automount *bool) string {
	if automount != nil && !*automount {
		return "unmounted"
	}
	return "mounted"
}

func (identities stepPodIdentities) defaultIdentity() stepPodIdentity {
	return stepPodIdentity{serviceAccount: identities.defaultAccount}
}

// resolve returns the identity for a container with this metadata. With no
// grants configured, or for a container no build owns, it is the default. A
// failed build lookup is a refusal: a pod is never created under an identity
// nobody could check.
func (identities stepPodIdentities) resolve(metadata db.ContainerMetadata) (stepPodIdentity, error) {
	if len(identities.grants) == 0 || identities.builds == nil ||
		metadata.Type == db.ContainerTypeCheck || metadata.BuildID == 0 {
		return identities.defaultIdentity(), nil
	}
	build, found, err := identities.builds.Build(metadata.BuildID)
	if err != nil {
		return stepPodIdentity{}, fmt.Errorf("resolve step pod identity for build %d: %w", metadata.BuildID, err)
	}
	if !found {
		return identities.defaultIdentity(), nil
	}
	owner, ok := stepPodOwnerOf(build)
	if !ok {
		return identities.defaultIdentity(), nil
	}
	for _, grant := range identities.grants {
		if grant.Owner == owner {
			identity := stepPodIdentity{grant: grant.Name, serviceAccount: identities.defaultAccount, privileged: grant.Privileged}
			if grant.ServiceAccount != "" {
				identity.serviceAccount, identity.ownAccount = grant.ServiceAccount, true
			}
			return identity, nil
		}
	}
	return identities.defaultIdentity(), nil
}
