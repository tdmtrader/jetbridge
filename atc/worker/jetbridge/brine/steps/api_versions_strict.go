package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/resourceserver/versionserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

type strictVersionsAPIObservation struct {
	Profile string
	Failure string
}

type strictVersionsHTTPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

func VersionsAPIStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictVersionsAPIObservation](
			"the production versions API behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictVersionsAPIObservation, error) {
				profile, err := paramAt("the production versions API behavior {string} is exercised", p, 0)
				if err != nil {
					return strictVersionsAPIObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictVersionsAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return strictVersionsAPIObservation{Profile: profile, Failure: observeStrictVersionsAPI(database, profile)}, nil
			},
		),
		brine.DefineCheck[strictVersionsAPIObservation](
			"the versions API behavior exactly matches {string}",
			func(in strictVersionsAPIObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the versions API behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeStrictVersionsAPI(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-versions-team"})
	if err != nil {
		return err.Error()
	}
	if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
		return err.Error()
	}
	if len(profile) >= len("clear-") && profile[:len("clear-")] == "clear-" {
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return err.Error()
		}
	}
	authorization, err := persistAPIAuthToken(database, "strict-versions-"+profile, "strict-versions-subject", time.Now().Add(time.Hour))
	if err != nil {
		return err.Error()
	}

	ref := atc.PipelineRef{Name: "strict-versions-pipeline"}
	config := strictVersionsAPIConfig(profile)
	pipeline, _, err := team.SavePipeline(ref, config, db.ConfigVersion(1), false)
	if err != nil {
		return err.Error()
	}
	if profile == "list-private-metadata-anonymous" {
		if err := pipeline.Expose(); err != nil {
			return err.Error()
		}
		authorization = ""
	}

	request := func(action string, params rata.Params, query url.Values) (strictVersionsHTTPResponse, error) {
		return runStrictVersionsHTTPRequest(database, action, params, query, authorization)
	}
	resourceParams := func(name string) rata.Params {
		return rata.Params{"team_name": team.Name(), "pipeline_name": pipeline.Name(), "resource_name": name}
	}

	switch profile {
	case "list-filter-all":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		values := []atc.Version{
			{"ref": "foo", "some-ref": "blah", "marker": "one"},
			{"ref": "foo", "some-ref": "wrong", "marker": "decoy-one"},
			{"ref": "foo", "some-ref": "blah", "marker": "two"},
			{"ref": "foo", "some-ref": "blah", "marker": "three"},
			{"ref": "wrong", "some-ref": "blah", "marker": "decoy-two"},
			{"ref": "foo", "some-ref": "blah", "marker": "four"},
		}
		ids, err := strictSaveResourceVersions(database, resource, values)
		if err != nil {
			return err.Error()
		}
		query := url.Values{"from": {strconv.Itoa(ids[0])}, "to": {strconv.Itoa(ids[5])}, "limit": {"2"}, "filter": {"ref:foo", "some-ref:blah"}}
		response, err := request(atc.ListResourceVersions, resourceParams("resource"), query)
		if err != nil {
			return err.Error()
		}
		got, err := strictDecodeVersions(response)
		if err != nil || !reflect.DeepEqual(strictVersionMarkers(got), []string{"two", "one"}) {
			return fail("bounded filtered markers=%v err=%v", strictVersionMarkers(got), err)
		}
		query.Del("from")
		response, err = request(atc.ListResourceVersions, resourceParams("resource"), query)
		if err != nil {
			return err.Error()
		}
		got, err = strictDecodeVersions(response)
		if err != nil || !reflect.DeepEqual(strictVersionMarkers(got), []string{"four", "three"}) {
			return fail("to-only filtered markers=%v err=%v", strictVersionMarkers(got), err)
		}
	case "list-filter-space", "list-filter-percent", "list-filter-colon":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		matching := atc.Version{"marker": "match"}
		decoy := atc.Version{"marker": "decoy"}
		filter := ""
		switch profile {
		case "list-filter-space":
			matching["some ref"], decoy["some ref"], filter = "some value", "other", "some ref:some value"
		case "list-filter-percent":
			matching["ref"], decoy["ref"], filter = "some%value", "some-value", "ref:some%value"
		case "list-filter-colon":
			matching["key"], decoy["key:with:colon"], filter = "with:colon:abcdef", "abcdef", "key:with:colon:abcdef"
		}
		if _, err := strictSaveResourceVersions(database, resource, []atc.Version{matching, decoy}); err != nil {
			return err.Error()
		}
		response, err := request(atc.ListResourceVersions, resourceParams("resource"), url.Values{"filter": {filter}})
		if err != nil {
			return err.Error()
		}
		got, err := strictDecodeVersions(response)
		if err != nil || !reflect.DeepEqual(strictVersionMarkers(got), []string{"match"}) {
			return fail("special filter markers=%v err=%v", strictVersionMarkers(got), err)
		}
	case "list-filter-invalid":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		if _, err := strictSaveResourceVersions(database, resource, []atc.Version{{"ref": "one"}, {"ref": "two"}}); err != nil {
			return err.Error()
		}
		response, err := request(atc.ListResourceVersions, resourceParams("resource"), url.Values{"filter": {"abcdef"}})
		if err != nil {
			return err.Error()
		}
		got, err := strictDecodeVersions(response)
		refs := strictVersionFieldValues(got, "ref")
		sort.Strings(refs)
		if err != nil || !reflect.DeepEqual(refs, []string{"one", "two"}) {
			return fail("invalid filter refs=%v err=%v", refs, err)
		}
	case "list-default-limit":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		values := make([]atc.Version, 101)
		for i := range values {
			values[i] = atc.Version{"ref": fmt.Sprintf("%03d", i)}
		}
		if _, err := strictSaveResourceVersions(database, resource, values); err != nil {
			return err.Error()
		}
		response, err := request(atc.ListResourceVersions, resourceParams("resource"), nil)
		if err != nil {
			return err.Error()
		}
		got, err := strictDecodeVersions(response)
		refs := strictVersionFieldValues(got, "ref")
		sort.Strings(refs)
		want := make([]string, atc.PaginationAPIDefaultLimit)
		for i := range want {
			want[i] = fmt.Sprintf("%03d", i+1)
		}
		if err != nil || !reflect.DeepEqual(refs, want) {
			return fail("default page count=%d refs=%v err=%v", len(got), refs, err)
		}
	case "list-json-metadata", "list-private-metadata-anonymous":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		older, newer := atc.Version{"ref": "older"}, atc.Version{"ref": "newer"}
		ids, err := strictSaveResourceVersions(database, resource, []atc.Version{older, newer})
		if err != nil {
			return err.Error()
		}
		metadata := atc.Metadata{{Name: "some", Value: "metadata"}}
		for _, version := range []atc.Version{older, newer} {
			updated, err := resource.UpdateMetadata(version, db.NewResourceConfigMetadataFields(metadata))
			if err != nil || !updated {
				return fail("update metadata changed=%t err=%v", updated, err)
			}
		}
		if err := resource.DisableVersion(ids[0]); err != nil {
			return err.Error()
		}
		response, err := request(atc.ListResourceVersions, resourceParams("resource"), nil)
		if err != nil {
			return err.Error()
		}
		got, err := strictDecodeVersions(response)
		want := []atc.ResourceVersion{
			{ID: ids[1], Enabled: true, Version: newer, Metadata: metadata},
			{ID: ids[0], Enabled: false, Version: older, Metadata: metadata},
		}
		if profile == "list-private-metadata-anonymous" {
			want[0].Metadata = nil
			want[1].Metadata = nil
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			return fail("metadata listing got=%#v want=%#v err=%v", got, want, err)
		}
	case "enable-exact", "disable-exact", "pin-exact", "pin-success":
		resource, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		values := []atc.Version{{"ref": "target"}, {"ref": "decoy"}}
		ids, err := strictSaveResourceVersions(database, resource, values)
		if err != nil {
			return err.Error()
		}
		action := atc.EnableResourceVersion
		if profile == "enable-exact" {
			if err := resource.DisableVersion(ids[0]); err != nil {
				return err.Error()
			}
			if err := resource.DisableVersion(ids[1]); err != nil {
				return err.Error()
			}
		} else if profile == "disable-exact" {
			action = atc.DisableResourceVersion
		} else {
			action = atc.PinResourceVersion
			pinned, err := resource.PinVersion(ids[1])
			if err != nil || !pinned {
				return fail("seed pin changed=%t err=%v", pinned, err)
			}
		}
		params := resourceParams("resource")
		params["resource_config_version_id"] = strconv.Itoa(ids[0])
		response, err := request(action, params, nil)
		if err != nil || response.Status != http.StatusOK {
			return fail("mutation status=%d err=%v", response.Status, err)
		}
		if profile == "pin-success" {
			response, err = request(action, params, nil)
			if err != nil || response.Status != http.StatusOK {
				return fail("repeat pin status=%d err=%v", response.Status, err)
			}
			params["resource_config_version_id"] = "-1"
			response, err = request(action, params, nil)
			if err != nil || response.Status != http.StatusInternalServerError {
				return fail("missing pin status=%d err=%v", response.Status, err)
			}
		}
		resource, err = strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		if profile == "enable-exact" || profile == "disable-exact" {
			got, err := strictListResourceVersions(resource)
			if err != nil {
				return err.Error()
			}
			enabled := map[int]bool{}
			for _, version := range got {
				enabled[version.ID] = version.Enabled
			}
			wantTarget := profile == "enable-exact"
			if enabled[ids[0]] != wantTarget || enabled[ids[1]] != !wantTarget {
				return fail("enabled states target=%t decoy=%t", enabled[ids[0]], enabled[ids[1]])
			}
		} else if !reflect.DeepEqual(resource.CurrentPinnedVersion(), values[0]) {
			return fail("pinned version=%v want=%v", resource.CurrentPinnedVersion(), values[0])
		}
	case "clear-resource-count", "clear-resource-target", "clear-resource-scope":
		target, err := strictVersionsResource(pipeline, "resource")
		if err != nil {
			return err.Error()
		}
		decoy, err := strictVersionsResource(pipeline, "decoy-resource")
		if err != nil {
			return err.Error()
		}
		if _, err := strictSaveResourceVersions(database, target, []atc.Version{{"ref": "v0"}, {"ref": "v1"}, {"ref": "v2"}}); err != nil {
			return err.Error()
		}
		if _, err := strictSaveResourceVersions(database, decoy, []atc.Version{{"ref": "decoy"}}); err != nil {
			return err.Error()
		}
		response, err := request(atc.ClearResourceVersions, resourceParams("resource"), nil)
		if err != nil || response.Status != http.StatusOK {
			return fail("clear resource status=%d err=%v", response.Status, err)
		}
		switch profile {
		case "clear-resource-count":
			var body atc.ClearVersionsResponse
			if err := json.Unmarshal(response.Body, &body); err != nil || body.VersionsRemoved != 3 {
				return fail("removed=%d err=%v", body.VersionsRemoved, err)
			}
		case "clear-resource-target":
			got, err := strictListResourceVersions(target)
			if err != nil || len(got) != 0 {
				return fail("target versions=%d err=%v", len(got), err)
			}
		case "clear-resource-scope":
			got, err := strictListResourceVersions(decoy)
			if err != nil || !reflect.DeepEqual(strictVersionFieldValues(got, "ref"), []string{"decoy"}) {
				return fail("decoy versions=%v err=%v", strictVersionFieldValues(got, "ref"), err)
			}
		}
	case "clear-type-count", "clear-type-target", "clear-type-scope":
		target, err := strictVersionsResourceType(pipeline, "resource-type")
		if err != nil {
			return err.Error()
		}
		decoy, err := strictVersionsResourceType(pipeline, "decoy-resource-type")
		if err != nil {
			return err.Error()
		}
		targetScope, targetIDs, err := strictSaveResourceTypeVersions(database, target, []atc.Version{{"ref": "v0"}, {"ref": "v1"}, {"ref": "v2"}})
		if err != nil {
			return err.Error()
		}
		decoyScope, _, err := strictSaveResourceTypeVersions(database, decoy, []atc.Version{{"ref": "decoy"}})
		if err != nil {
			return err.Error()
		}
		params := rata.Params{"team_name": team.Name(), "pipeline_name": pipeline.Name(), "resource_type_name": "resource-type"}
		response, err := request(atc.ClearResourceTypeVersions, params, nil)
		if err != nil || response.Status != http.StatusOK {
			return fail("clear resource type status=%d err=%v", response.Status, err)
		}
		switch profile {
		case "clear-type-count":
			var body atc.ClearVersionsResponse
			if err := json.Unmarshal(response.Body, &body); err != nil || body.VersionsRemoved != 3 {
				return fail("removed=%d err=%v", body.VersionsRemoved, err)
			}
		case "clear-type-target":
			for _, version := range []atc.Version{{"ref": "v0"}, {"ref": "v1"}, {"ref": "v2"}} {
				_, found, err := targetScope.FindVersion(version)
				if err != nil || found {
					return fail("target version %v found=%t err=%v ids=%v", version, found, err, targetIDs)
				}
			}
		case "clear-type-scope":
			_, found, err := decoyScope.FindVersion(atc.Version{"ref": "decoy"})
			if err != nil || !found {
				return fail("decoy version found=%t err=%v", found, err)
			}
		}
	default:
		return fail("unknown versions API profile %q", profile)
	}
	return ""
}

func strictVersionsAPIConfig(profile string) atc.Config {
	switch {
	case len(profile) >= len("clear-resource-") && profile[:len("clear-resource-")] == "clear-resource-":
		return atc.Config{Resources: atc.ResourceConfigs{
			{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "strict-clear-target"}},
			{Name: "decoy-resource", Type: "registry-image", Source: atc.Source{"repository": "strict-clear-decoy"}},
		}}
	case len(profile) >= len("clear-type-") && profile[:len("clear-type-")] == "clear-type-":
		return atc.Config{ResourceTypes: atc.ResourceTypes{
			{Name: "resource-type", Type: "registry-image", Source: atc.Source{"repository": "strict-type-target"}},
			{Name: "decoy-resource-type", Type: "registry-image", Source: atc.Source{"repository": "strict-type-decoy"}},
		}}
	default:
		return atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "strict-versions"}}}}
	}
}

func strictVersionsResource(pipeline db.Pipeline, name string) (db.Resource, error) {
	resource, found, err := pipeline.Resource(name)
	if err != nil || !found {
		return nil, fmt.Errorf("load resource %q: found=%t err=%w", name, found, err)
	}
	return resource, nil
}

func strictVersionsResourceType(pipeline db.Pipeline, name string) (db.ResourceType, error) {
	resourceType, found, err := pipeline.ResourceType(name)
	if err != nil || !found {
		return nil, fmt.Errorf("load resource type %q: found=%t err=%w", name, found, err)
	}
	return resourceType, nil
}

func strictSaveResourceVersions(database JetbridgeDB, resource db.Resource, versions []atc.Version) ([]int, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(
		resource.Type(), resource.Source(), nil,
	)
	if err != nil {
		return nil, fmt.Errorf("find or create resource config: %w", err)
	}
	id := resource.ID()
	scope, err := config.FindOrCreateScope(&id)
	if err != nil {
		return nil, err
	}
	if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return nil, err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return nil, fmt.Errorf("set resource config scope: %w", err)
	}
	if found, err := resource.Reload(); err != nil || !found {
		return nil, fmt.Errorf("reload scoped resource: found=%t err=%w", found, err)
	}
	ids := make([]int, len(versions))
	for i, version := range versions {
		row, found, err := scope.FindVersion(version)
		if err != nil || !found {
			return nil, fmt.Errorf("load version %v: found=%t err=%w", version, found, err)
		}
		ids[i] = row.ID()
	}
	return ids, nil
}

func strictSaveResourceTypeVersions(database JetbridgeDB, resourceType db.ResourceType, versions []atc.Version) (db.ResourceConfigScope, []int, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(
		resourceType.Type(), resourceType.Source(), nil,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("find or create resource type config: %w", err)
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return nil, nil, err
	}
	if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return nil, nil, err
	}
	if err := resourceType.SetResourceConfigScope(scope); err != nil {
		return nil, nil, fmt.Errorf("set resource type config scope: %w", err)
	}
	if found, err := resourceType.Reload(); err != nil || !found {
		return nil, nil, fmt.Errorf("reload scoped resource type: found=%t err=%w", found, err)
	}
	ids := make([]int, len(versions))
	for i, version := range versions {
		row, found, err := scope.FindVersion(version)
		if err != nil || !found {
			return nil, nil, fmt.Errorf("load resource type version %v: found=%t err=%w", version, found, err)
		}
		ids[i] = row.ID()
	}
	return scope, ids, nil
}

func strictListResourceVersions(resource db.Resource) ([]atc.ResourceVersion, error) {
	versions, _, found, err := resource.Versions(db.Page{Limit: 200}, nil)
	if err != nil || !found {
		return nil, fmt.Errorf("list resource versions: found=%t err=%w", found, err)
	}
	return versions, nil
}

func strictDecodeVersions(response strictVersionsHTTPResponse) ([]atc.ResourceVersion, error) {
	if response.Status != http.StatusOK {
		return nil, fmt.Errorf("list status=%d body=%q", response.Status, response.Body)
	}
	var versions []atc.ResourceVersion
	if err := json.Unmarshal(response.Body, &versions); err != nil {
		return nil, err
	}
	return versions, nil
}

func strictVersionMarkers(versions []atc.ResourceVersion) []string {
	return strictVersionFieldValues(versions, "marker")
}

func strictVersionFieldValues(versions []atc.ResourceVersion, field string) []string {
	values := make([]string, len(versions))
	for i, version := range versions {
		values[i] = version.Version[field]
	}
	return values
}

func runStrictVersionsHTTPRequest(database JetbridgeDB, action string, params rata.Params, query url.Values, authorization string) (strictVersionsHTTPResponse, error) {
	logger := lager.NewLogger("brine-versions-api")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return strictVersionsHTTPResponse{}, err
	}
	versions := versionserver.NewServer(logger, "https://example.com")
	scoped := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListResourceVersions:      scoped.HandlerFor(versions.ListResourceVersions),
		atc.EnableResourceVersion:     scoped.HandlerFor(versions.EnableResourceVersion),
		atc.DisableResourceVersion:    scoped.HandlerFor(versions.DisableResourceVersion),
		atc.PinResourceVersion:        scoped.HandlerFor(versions.PinResourceVersion),
		atc.ClearResourceVersions:     scoped.HandlerFor(versions.ClearResourceVersions),
		atc.ClearResourceTypeVersions: scoped.HandlerFor(versions.ClearResourceTypeVersions),
	}
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory), nil, nil, nil),
		wrappa.NewAccessorWrappa(logger, accessFactory, auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger), map[string]string{}),
	}
	wrapped := wrapper.Wrap(handlers)
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, found := wrapped[route.Name]; found {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapped)
	if err != nil {
		return strictVersionsHTTPResponse{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return strictVersionsHTTPResponse{}, err
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	shutdown := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(ctx)
		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return shutdownErr
	}

	generator := rata.NewRequestGenerator("http://"+listener.Addr().String(), atc.Routes)
	request, err := generator.CreateRequest(action, params, nil)
	if err != nil {
		_ = shutdown()
		return strictVersionsHTTPResponse{}, err
	}
	values := request.URL.Query()
	for key, entries := range query {
		for _, entry := range entries {
			values.Add(key, entry)
		}
	}
	request.URL.RawQuery = values.Encode()
	request.Header.Set("Authorization", authorization)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		_ = shutdown()
		return strictVersionsHTTPResponse{}, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	shutdownErr := shutdown()
	if readErr != nil {
		return strictVersionsHTTPResponse{}, readErr
	}
	if closeErr != nil {
		return strictVersionsHTTPResponse{}, closeErr
	}
	if shutdownErr != nil {
		return strictVersionsHTTPResponse{}, shutdownErr
	}
	return strictVersionsHTTPResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: body}, nil
}
