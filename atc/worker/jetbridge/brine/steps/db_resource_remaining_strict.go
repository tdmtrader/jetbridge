package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceRemainingObservation struct{ Value string }

func DBResourceRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceRemainingObservation](
			"the production resource database evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceRemainingObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceRemainingObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBResourceRemaining(database, profile)
				return DBResourceRemainingObservation{Value: value}, err
			},
		),
		brine.DefineCheck[DBResourceRemainingObservation](
			"the resource database observation is {string}",
			func(in DBResourceRemainingObservation, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetString(0)
				if in.Value != want {
					return fmt.Errorf("expected resource observation %q, got %q", want, in.Value)
				}
				return nil
			},
		),
	}
}

func observeDBResourceRemaining(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "resources-list", "resource-existing", "resource-missing":
		return observeResourceIdentity(database, profile)
	case "public-default", "public-true", "public-false":
		return observeResourcePublic(database, profile)
	case "filter-one-match", "filter-one-miss", "filter-two-match", "filter-two-miss":
		return observeResourceFilters(database, profile)
	case "page-first", "page-to-middle", "page-to-oldest", "page-from-middle", "page-from-newest",
		"metadata-returned", "metadata-maintained", "metadata-updated", "disabled-returned", "metadata-update-visible":
		return observeResourceOrderedVersions(database, profile)
	case "check-order", "check-order-from", "check-order-to":
		return observeResourceCheckOrder(database, profile)
	default:
		return "", fmt.Errorf("unknown resource database profile %q", profile)
	}
}

func observeResourceIdentity(database JetbridgeDB, profile string) (string, error) {
	pipeline, err := saveResourcePipeline(database, atc.Config{Resources: atc.ResourceConfigs{
		{Name: "some-resource", Type: "registry-image", WebhookToken: "some-token", Source: atc.Source{"some": "repository"}, Version: atc.Version{"ref": "abcdef"}, CheckTimeout: "999m"},
		{Name: "some-other-resource", Public: true, Type: "git", Source: atc.Source{"some": "other-repository"}},
		{Name: "some-secret-resource", Type: "git", Source: atc.Source{"some": "((secret-repository))"}},
		{Name: "some-resource-custom-check", Type: "git", Source: atc.Source{"some": "some-repository"}, CheckEvery: &atc.CheckEvery{Interval: 10_000_000}, CheckTimeout: "1m"},
	}})
	if err != nil {
		return "", err
	}
	if profile == "resource-missing" {
		resource, found, err := pipeline.Resource("bonkers")
		return fmt.Sprintf("found=%t;nil=%t", found, resource == nil), err
	}
	if profile == "resource-existing" {
		resource, found, err := pipeline.Resource("some-resource")
		if err != nil || !found {
			return "", fmt.Errorf("load resource: found=%t: %w", found, err)
		}
		valid := resource.Name() == "some-resource" && resource.Type() == "registry-image" && fmt.Sprint(resource.Source()["some"]) == "repository"
		return fmt.Sprintf("valid=%t", valid), nil
	}
	resources, err := pipeline.Resources()
	if err != nil {
		return "", err
	}
	ids := map[int]bool{}
	valid := len(resources) == 4
	for _, resource := range resources {
		ids[resource.ID()] = true
		switch resource.Name() {
		case "some-resource":
			valid = valid && resource.Type() == "registry-image" && fmt.Sprint(resource.Source()["some"]) == "repository" && resource.ConfigPinnedVersion()["ref"] == "abcdef" && resource.CurrentPinnedVersion()["ref"] == "abcdef" && resource.HasWebhook()
		case "some-other-resource":
			valid = valid && resource.Type() == "git" && fmt.Sprint(resource.Source()["some"]) == "other-repository" && !resource.HasWebhook()
		case "some-secret-resource":
			valid = valid && resource.Type() == "git" && fmt.Sprint(resource.Source()["some"]) == "((secret-repository))" && !resource.HasWebhook()
		case "some-resource-custom-check":
			valid = valid && resource.Type() == "git" && fmt.Sprint(resource.Source()["some"]) == "some-repository" && resource.CheckEvery().Interval.String() == "10ms" && resource.CheckTimeout() == "1m" && !resource.HasWebhook()
		default:
			valid = false
		}
	}
	return fmt.Sprintf("valid=%t", valid && len(ids) == 4), nil
}

func observeResourcePublic(database JetbridgeDB, profile string) (string, error) {
	public := profile == "public-true"
	pipeline, err := saveResourcePipeline(database, atc.Config{Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "registry-image", Public: public}}})
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("some-resource")
	if err != nil || !found {
		return "", fmt.Errorf("load public resource: found=%t: %w", found, err)
	}
	return fmt.Sprintf("public=%t", resource.Public()), nil
}

type remainingResourceFixture struct {
	resource db.Resource
	scope    db.ResourceConfigScope
}

func saveResourcePipeline(database JetbridgeDB, config atc.Config) (db.Pipeline, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "resource-remaining-team"})
	if err != nil {
		return nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "resource-remaining-pipeline"}, config, 0, false)
	return pipeline, err
}

func resourceVersionScenario(database JetbridgeDB, versions ...atc.Version) (*remainingResourceFixture, error) {
	pipeline, err := saveResourcePipeline(database, atc.Config{Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "repository"}}}})
	if err != nil {
		return nil, err
	}
	resource, found, err := pipeline.Resource("some-resource")
	if err != nil || !found {
		return nil, fmt.Errorf("load version resource: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return nil, err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return nil, err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return nil, err
	}
	if found, err := resource.Reload(); err != nil || !found {
		return nil, fmt.Errorf("reload scoped resource: found=%t: %w", found, err)
	}
	if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return nil, err
	}
	return &remainingResourceFixture{resource: resource, scope: scope}, nil
}

func observeResourceFilters(database JetbridgeDB, profile string) (string, error) {
	fixture, err := resourceVersionScenario(database,
		atc.Version{"ref": "v0", "commit": "v0"}, atc.Version{"ref": "v1", "commit": "v1"}, atc.Version{"ref": "v2", "commit": "v2"})
	if err != nil {
		return "", err
	}
	filters := map[string]atc.Version{
		"filter-one-match": {"ref": "v2"}, "filter-one-miss": {"ref": "v20"},
		"filter-two-match": {"ref": "v1", "commit": "v1"}, "filter-two-miss": {"ref": "v1", "commit": "v2"},
	}
	versions, _, found, err := fixture.resource.Versions(db.Page{Limit: 10}, filters[profile])
	return fmt.Sprintf("error=%t;found=%t;count=%d;refs=%s", err != nil, found, len(versions), resourceRefs(versions)), nil
}

func observeResourceOrderedVersions(database JetbridgeDB, profile string) (string, error) {
	input := make([]atc.Version, 10)
	for i := range input {
		input[i] = atc.Version{"ref": fmt.Sprintf("v%d", i)}
	}
	fixture, err := resourceVersionScenario(database, input...)
	if err != nil {
		return "", err
	}
	resource := fixture.resource
	ids := make([]int, 10)
	for i, version := range input {
		persisted, found, err := fixture.scope.FindVersion(version)
		if err != nil || !found {
			return "", fmt.Errorf("find version %d: found=%t: %w", i, found, err)
		}
		ids[i] = persisted.ID()
	}
	if strings.HasPrefix(profile, "metadata-") || profile == "disabled-returned" {
		metadata := db.ResourceConfigMetadataFields{{Name: "name1", Value: "value1"}}
		if _, err := resource.UpdateMetadata(input[9], metadata); err != nil {
			return "", err
		}
		switch profile {
		case "metadata-maintained":
			if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{input[9]}); err != nil {
				return "", err
			}
		case "metadata-updated":
			if _, err := resource.UpdateMetadata(input[9], db.ResourceConfigMetadataFields{{Name: "name-new", Value: "value-new"}}); err != nil {
				return "", err
			}
		case "disabled-returned":
			if err := resource.DisableVersion(ids[9]); err != nil {
				return "", err
			}
		}
		versions, _, found, err := resource.Versions(db.Page{Limit: 1}, nil)
		ref := ""
		enabled := false
		if len(versions) > 0 {
			ref = fmt.Sprint(versions[0].Version["ref"])
			enabled = versions[0].Enabled
		}
		if profile == "disabled-returned" {
			return fmt.Sprintf("error=%t;found=%t;count=%d;ref=%s;enabled=%t", err != nil, found, len(versions), ref, enabled), nil
		}
		metadataObservation := ""
		if len(versions) > 0 && len(versions[0].Metadata) > 0 {
			metadataObservation = versions[0].Metadata[0].Name + ":" + versions[0].Metadata[0].Value
		}
		return fmt.Sprintf("error=%t;found=%t;count=%d;ref=%s;metadata=%s", err != nil, found, len(versions), ref, metadataObservation), nil
	}
	page := db.Page{Limit: 2}
	switch profile {
	case "page-to-middle":
		page.To = db.NewIntPtr(ids[6])
	case "page-to-oldest":
		page.To = db.NewIntPtr(ids[1])
	case "page-from-middle":
		page.From = db.NewIntPtr(ids[6])
	case "page-from-newest":
		page.From = db.NewIntPtr(ids[8])
	}
	versions, pagination, found, err := resource.Versions(page, nil)
	return resourcePageObservation(err, found, versions, pagination, ids), nil
}

func observeResourceCheckOrder(database JetbridgeDB, profile string) (string, error) {
	fixture, err := resourceVersionScenario(database, atc.Version{"ref": "v1"}, atc.Version{"ref": "v3"}, atc.Version{"ref": "v4"})
	if err != nil {
		return "", err
	}
	if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v2"}, {"ref": "v3"}, {"ref": "v4"}}); err != nil {
		return "", err
	}
	ids := make([]int, 5)
	for i := 1; i <= 4; i++ {
		persisted, found, err := fixture.scope.FindVersion(atc.Version{"ref": fmt.Sprintf("v%d", i)})
		if err != nil || !found {
			return "", fmt.Errorf("find check-order version %d: found=%t: %w", i, found, err)
		}
		ids[i] = persisted.ID()
	}
	page := db.Page{Limit: 4}
	if profile == "check-order-from" {
		page = db.Page{From: db.NewIntPtr(ids[2]), Limit: 2}
	} else if profile == "check-order-to" {
		page = db.Page{To: db.NewIntPtr(ids[3]), Limit: 2}
	}
	versions, pagination, found, err := fixture.resource.Versions(page, nil)
	return resourcePageObservation(err, found, versions, pagination, ids), nil
}

func resourceRefs(versions []atc.ResourceVersion) string {
	refs := make([]string, 0, len(versions))
	for _, version := range versions {
		refs = append(refs, fmt.Sprint(version.Version["ref"]))
	}
	return strings.Join(refs, ",")
}

func resourcePageObservation(err error, found bool, versions []atc.ResourceVersion, pagination db.Pagination, ids []int) string {
	refForID := func(id int) string {
		for index, candidate := range ids {
			if candidate == id {
				return fmt.Sprintf("v%d", index)
			}
		}
		return "unknown"
	}
	page := func(value *db.Page, cursor func(*db.Page) *int) string {
		if value == nil {
			return "nil"
		}
		id := cursor(value)
		if id == nil {
			return fmt.Sprintf("invalid/%d", value.Limit)
		}
		return fmt.Sprintf("%s/%d", refForID(*id), value.Limit)
	}
	return fmt.Sprintf("error=%t;found=%t;refs=%s;newer=%s;older=%s", err != nil, found, resourceRefs(versions), page(pagination.Newer, func(p *db.Page) *int { return p.From }), page(pagination.Older, func(p *db.Page) *int { return p.To }))
}
