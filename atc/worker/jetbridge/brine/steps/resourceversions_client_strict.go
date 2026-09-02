package steps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/resourceserver"
	"github.com/concourse/concourse/atc/api/resourceserver/versionserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

type strictResourceVersionsClientObservation struct {
	Profile string
	Failure string
}

type strictResourceVersionsClientBoundary struct {
	team     db.Team
	pipeline db.Pipeline
	resource db.Resource
	ref      atc.PipelineRef
	versions map[string]int
	client   clientapi.Team
}

func ResourceVersionsClientStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictResourceVersionsClientObservation](
			"the strict production resource-version client behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (strictResourceVersionsClientObservation, error) {
				profile, err := paramAt("the strict production resource-version client behavior {string} is exercised", p, 0)
				if err != nil {
					return strictResourceVersionsClientObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictResourceVersionsClientObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictResourceVersionsClientBoundary(database, rec)
				if err != nil {
					return strictResourceVersionsClientObservation{}, err
				}
				return strictResourceVersionsClientObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[strictResourceVersionsClientObservation](
			"the strict resource-version client behavior exactly matches {string}",
			func(in strictResourceVersionsClientObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the strict resource-version client behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
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

func newStrictResourceVersionsClientBoundary(database JetbridgeDB, rec *brine.Recorder) (*strictResourceVersionsClientBoundary, error) {
	logger := lager.NewLogger("brine-resourceversions-client-strict")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "resourceversions-client-team"})
	if err != nil {
		return nil, err
	}
	if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
		return nil, err
	}
	if err := makeAPIAuthAdmin(database, team); err != nil {
		return nil, err
	}
	ref := atc.PipelineRef{Name: "target", InstanceVars: atc.InstanceVars{"branch": "master"}}
	config := atc.Config{
		Resources:     atc.ResourceConfigs{{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "strict/client"}}},
		ResourceTypes: atc.ResourceTypes{{Name: "custom", Type: "registry-image", Source: atc.Source{"repository": "strict/type"}}},
	}
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	if err != nil {
		return nil, err
	}
	if _, _, err := team.SavePipeline(atc.PipelineRef{Name: ref.Name}, atc.Config{}, 0, false); err != nil {
		return nil, err
	}
	resource, err := strictVersionsResource(pipeline, "image")
	if err != nil {
		return nil, err
	}
	values := []atc.Version{{"ref": "1"}, {"ref": "2"}, {"ref": "3"}, {"ref": "4"}, {"ref": "5"}}
	ids, err := strictSaveResourceVersions(database, resource, values)
	if err != nil {
		return nil, err
	}
	versions := map[string]int{}
	for i, id := range ids {
		versions[fmt.Sprint(i+1)] = id
	}
	resourceType, err := strictVersionsResourceType(pipeline, "custom")
	if err != nil {
		return nil, err
	}
	if _, _, err := strictSaveResourceTypeVersions(database, resourceType, []atc.Version{{"ref": "a"}, {"ref": "b"}}); err != nil {
		return nil, err
	}

	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return nil, err
	}
	versionsServer := versionserver.NewServer(logger, "https://example.invalid")
	resourceServer := resourceserver.NewServer(logger, nil, nil, nil, nil, nil)
	scoped := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListResourceVersions:      scoped.HandlerFor(versionsServer.ListResourceVersions),
		atc.EnableResourceVersion:     scoped.HandlerFor(versionsServer.EnableResourceVersion),
		atc.DisableResourceVersion:    scoped.HandlerFor(versionsServer.DisableResourceVersion),
		atc.PinResourceVersion:        scoped.HandlerFor(versionsServer.PinResourceVersion),
		atc.ClearResourceVersions:     scoped.HandlerFor(versionsServer.ClearResourceVersions),
		atc.ClearResourceTypeVersions: scoped.HandlerFor(versionsServer.ClearResourceTypeVersions),
		atc.UnpinResource:             scoped.HandlerFor(resourceServer.UnpinResource),
		atc.SetPinCommentOnResource:   scoped.HandlerFor(resourceServer.SetPinCommentOnResource),
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
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	httpServer := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	authorization, err := persistAPIAuthToken(database, "resourceversions-client-token", "strict-resourceversions-subject", time.Now().Add(time.Hour))
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	token := strings.TrimPrefix(authorization, "bearer ")
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}))
	httpClient.Timeout = 30 * time.Second
	rec.RegisterDisposer(func() {
		httpClient.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
		}
	})
	client := clientapi.NewClient("http://"+listener.Addr().String(), httpClient, false).Team(team.Name())
	return &strictResourceVersionsClientBoundary{team: team, pipeline: pipeline, resource: resource, ref: ref, versions: versions, client: client}, nil
}

func (b *strictResourceVersionsClientBoundary) observe(profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	id := func(ref string) int { return b.versions[ref] }
	refs := func(versions []atc.ResourceVersion) []string {
		result := make([]string, len(versions))
		for i, version := range versions {
			result[i] = version.Version["ref"]
		}
		sort.Strings(result)
		return result
	}
	page := clientapi.Page{}
	wantRefs := []string{"1", "2", "3", "4", "5"}
	switch profile {
	case "list-from":
		page.From, page.Limit, wantRefs = id("1"), 2, []string{"1", "2"}
	case "list-from-limit":
		page.From, page.Limit, wantRefs = id("1"), 2, []string{"1", "2"}
	case "list-to":
		page.To, page.Limit, wantRefs = id("3"), 2, []string{"2", "3"}
	case "list-to-limit":
		page.To, page.Limit, wantRefs = id("3"), 2, []string{"2", "3"}
	case "list-from-to":
		page.From, page.To, page.Limit, wantRefs = id("2"), id("4"), 2, []string{"2", "3"}
	}
	if profile == "pagination-links" {
		_, nextPagination, found, err := b.client.ResourceVersions(b.ref, "image", clientapi.Page{Limit: 2}, nil)
		if err != nil || !found || nextPagination.Next == nil {
			return fail("next pagination=%+v found=%t err=%v", nextPagination, found, err)
		}
		_, previousPagination, found, err := b.client.ResourceVersions(b.ref, "image", clientapi.Page{From: id("1"), Limit: 2}, nil)
		if err != nil || !found || previousPagination.Previous == nil {
			return fail("previous pagination=%+v found=%t err=%v", previousPagination, found, err)
		}
		return ""
	}
	if (strings.HasPrefix(profile, "list-") && profile != "list-not-found" && profile != "list-filter") || profile == "pagination-empty" {
		versions, pagination, found, err := b.client.ResourceVersions(b.ref, "image", page, nil)
		if err != nil || !found || !reflect.DeepEqual(refs(versions), wantRefs) {
			return fail("list refs=%v found=%t err=%v", refs(versions), found, err)
		}
		if profile == "pagination-links" {
			if pagination.Next == nil || pagination.Previous == nil {
				return fail("pagination=%+v", pagination)
			}
		} else if profile == "pagination-empty" && (pagination.Next != nil || pagination.Previous != nil) {
			return fail("pagination=%+v", pagination)
		}
		return ""
	}
	switch profile {
	case "list-filter":
		versions, _, found, err := b.client.ResourceVersions(b.ref, "image", clientapi.Page{}, atc.Version{"ref": "3"})
		if err != nil || !found || !reflect.DeepEqual(refs(versions), []string{"3"}) {
			return fail("filter refs=%v found=%t err=%v", refs(versions), found, err)
		}
	case "list-not-found":
		_, _, found, err := b.client.ResourceVersions(b.ref, "missing", clientapi.Page{}, nil)
		if err != nil || found {
			return fail("missing found=%t err=%v", found, err)
		}
	case "disable-success", "enable-success", "pin-success", "pin-error":
		versionID := id("3")
		if profile == "pin-error" {
			versionID = -1
		}
		if profile == "enable-success" {
			if err := b.resource.DisableVersion(versionID); err != nil {
				return fail("seed disable err=%v", err)
			}
		}
		var changed bool
		var err error
		switch profile {
		case "disable-success":
			changed, err = b.client.DisableResourceVersion(b.ref, "image", versionID)
		case "enable-success":
			changed, err = b.client.EnableResourceVersion(b.ref, "image", versionID)
		default:
			changed, err = b.client.PinResourceVersion(b.ref, "image", versionID)
		}
		if profile == "pin-error" {
			if err == nil || changed {
				return fail("pin error changed=%t err=%v", changed, err)
			}
			return ""
		}
		if err != nil || !changed {
			return fail("mutation changed=%t err=%v", changed, err)
		}
	case "disable-not-found", "enable-not-found", "pin-not-found":
		var changed bool
		var err error
		switch profile {
		case "disable-not-found":
			changed, err = b.client.DisableResourceVersion(b.ref, "missing", id("3"))
		case "enable-not-found":
			changed, err = b.client.EnableResourceVersion(b.ref, "missing", id("3"))
		default:
			changed, err = b.client.PinResourceVersion(b.ref, "missing", id("3"))
		}
		if err != nil || changed {
			return fail("missing changed=%t err=%v", changed, err)
		}
	case "unpin-success":
		if pinned, err := b.resource.PinVersion(id("3")); err != nil || !pinned {
			return fail("seed pin=%t err=%v", pinned, err)
		}
		changed, err := b.client.UnpinResource(b.ref, "image")
		if err != nil || !changed {
			return fail("unpin=%t err=%v", changed, err)
		}
	case "unpin-not-found":
		changed, err := b.client.UnpinResource(b.ref, "missing")
		if err != nil || changed {
			return fail("missing unpin=%t err=%v", changed, err)
		}
	case "comment-success":
		pinned, err := b.resource.PinVersion(id("3"))
		if err != nil || !pinned {
			return fail("seed pin=%t err=%v", pinned, err)
		}
		changed, err := b.client.SetPinComment(b.ref, "image", "some comment")
		if err != nil || !changed {
			return fail("comment=%t err=%v", changed, err)
		}
		resource, err := strictVersionsResource(b.pipeline, "image")
		if err == nil {
			_, err = resource.Reload()
		}
		if err != nil || resource.PinComment() != "some comment" {
			return fail("stored comment=%q err=%v", resource.PinComment(), err)
		}
	case "comment-not-found":
		changed, err := b.client.SetPinComment(b.ref, "missing", "some comment")
		if err != nil || changed {
			return fail("missing comment=%t err=%v", changed, err)
		}
	case "clear-resource-success":
		removed, err := b.client.ClearResourceVersions(b.ref, "image")
		if err != nil || removed != 5 {
			return fail("removed=%d err=%v", removed, err)
		}
	case "clear-resource-error":
		_, err := b.client.ClearResourceVersions(b.ref, "missing")
		if err == nil {
			return "missing clear resource error"
		}
	case "clear-type-success":
		removed, err := b.client.ClearResourceTypeVersions(b.ref, "custom")
		if err != nil || removed != 2 {
			return fail("removed=%d err=%v", removed, err)
		}
	case "clear-type-error":
		_, err := b.client.ClearResourceTypeVersions(b.ref, "missing")
		if err == nil {
			return "missing clear resource type error"
		}
	default:
		return fail("unknown profile %q", profile)
	}
	return ""
}
