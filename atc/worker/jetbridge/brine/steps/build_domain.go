package steps

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
)

type BuildDomainObservation struct{ Value string }

func BuildDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, BuildDomainObservation](
			"the real build domain evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (BuildDomainObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return BuildDomainObservation{}, fmt.Errorf("expected build domain profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return BuildDomainObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeBuildDomain(database, profile)
				return BuildDomainObservation{Value: value}, err
			},
		),
		CheckString[BuildDomainObservation]("the build domain observation is {string}", "build domain observation",
			func(in BuildDomainObservation) (string, error) { return in.Value, nil }),
		brine.DefineCheck[BuildDomainObservation](
			"the build domain observation contains {string}",
			func(in BuildDomainObservation, p brine.Params, _ *brine.Recorder) error {
				expected, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected build domain fragments")
				}
				for _, fragment := range strings.Split(expected, " ;; ") {
					if !strings.Contains(in.Value, fragment) {
						return fmt.Errorf("build domain observation %q does not contain %q", in.Value, fragment)
					}
				}
				return nil
			},
		),
	}
}

func observeBuildDomain(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "build-team"})
	if err != nil {
		return "", err
	}
	build, err := saveAuthBuild(team)
	if err != nil {
		return "", err
	}
	switch profile {
	case "creation-metadata":
		createdBy := ""
		if build.CreatedBy() != nil {
			createdBy = *build.CreatedBy()
		}
		owner := build.ContainerOwner("some-plan")
		ownerMap, err := owner.Create(nil, "")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("created-by=%s;recent=%t;run-state=build:%d;teams=%s;cache-build=%v;owner-build=%v;owner-plan=%v;owner-team=%v",
			createdBy, time.Since(build.CreateTime()) < time.Second, build.ID(), strings.Join(build.AllAssociatedTeamNames(), ","),
			build.ResourceCacheUser().SQLMap()["build_id"], ownerMap["build_id"], ownerMap["plan_id"], ownerMap["team_id"]), nil
	case "one-off-no-plan":
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("has-plan=%t", oneOff.HasPlan()), nil
	case "comment-round-trip":
		if err := build.SetComment("hello-world"); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		first := build.Comment()
		if err := build.SetComment("updated-comment"); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return "first=" + first + ";second=" + build.Comment(), nil
	case "lager/one-off":
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return canonicalAnyMap(oneOff.LagerData()), nil
	case "lager/job":
		return canonicalAnyMap(build.LagerData()), nil
	case "syslog/one-off":
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return oneOff.SyslogTag(event.OriginID("origin")), nil
	case "syslog/job":
		return build.SyslogTag(event.OriginID("origin")), nil
	case "tracing/one-off":
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return canonicalStringMap(oneOff.TracingAttrs()), nil
	case "tracing/job":
		return canonicalStringMap(build.TracingAttrs()), nil
	case "reload-after-start":
		started, err := build.Start(atc.Plan{})
		if err != nil {
			return "", err
		}
		before := build.Status()
		found, err := build.Reload()
		return fmt.Sprintf("started=%t;before=%s;found=%t;after=%s", started, before, found, build.Status()), err
	case "drain":
		before := build.IsDrained()
		if err := build.SetDrained(true); err != nil {
			return "", err
		}
		immediate := build.IsDrained()
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("before=%t;immediate=%t;reloaded=%t", before, immediate, build.IsDrained()), nil
	case "start-aborted":
		if err := build.MarkAsAborted(); err != nil {
			return "", err
		}
		started, err := build.Start(atc.Plan{ID: "plan"})
		if err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("started=%t;status=%s", started, build.Status()), nil
	case "start-success":
		plan := atc.Plan{ID: "plan", Get: &atc.GetPlan{Name: "input", Type: "git", Resource: "repo"}}
		started, err := build.Start(plan)
		if err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("started=%t;status=%s;has-plan=%t;public-plan=%t", started, build.Status(), build.HasPlan(), reflect.DeepEqual(build.PublicPlan(), plan.Public())), nil
	case "mark-aborted":
		if err := build.MarkAsAborted(); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("aborted=%t", build.IsAborted()), nil
	case "pipeline":
		pipeline, found, err := build.Pipeline()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("found=%t;name=%s", found, pipeline.Name()), nil
	default:
		return "", fmt.Errorf("unknown build domain profile %q", profile)
	}
}

func canonicalAnyMap(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprint(values[key]))
	}
	return strings.Join(parts, ";")
}

func canonicalStringMap(values map[string]string) string {
	converted := make(map[string]any, len(values))
	for key, value := range values {
		converted[key] = value
	}
	return canonicalAnyMap(converted)
}
