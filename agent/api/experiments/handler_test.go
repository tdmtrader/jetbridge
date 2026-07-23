package experiments_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

const largeExperimentID experiment.ID = 9007199254740993

type experimentStore struct {
	create    func(context.Context, int, string, string, experiment.Definition) (experimentsapi.StoredExperiment, error)
	update    func(context.Context, int, experiment.ID, int64, string, experiment.Definition) (experimentsapi.StoredExperiment, error)
	get       func(context.Context, int, experiment.ID) (experimentsapi.StoredExperiment, bool, error)
	list      func(context.Context, int) ([]experimentsapi.StoredExperiment, error)
	listPage  func(context.Context, int, experiment.ListFilter) ([]experimentsapi.StoredExperiment, error)
	preflight func(context.Context, int, experiment.ID, int64) (experimentsapi.StoredExperiment, error)
	start     func(context.Context, int, experiment.ID, int64, string) (experimentsapi.StoredExperiment, error)
	cancel    func(context.Context, int, experiment.ID, int64, string) (experimentsapi.StoredExperiment, error)
	listCells func(context.Context, int, experiment.ID) ([]experimentsapi.StoredCell, error)
	getCell   func(context.Context, int, experiment.ID, experiment.CellID) (experimentsapi.StoredCell, bool, error)
	scorecard func(context.Context, int, experiment.ID) (experiment.Scorecard, error)
}

func (store *experimentStore) Create(ctx context.Context, teamID int, teamName, actor string, definition experiment.Definition) (experimentsapi.StoredExperiment, error) {
	return store.create(ctx, teamID, teamName, actor, definition)
}
func (store *experimentStore) Update(ctx context.Context, teamID int, id experiment.ID, revision int64, actor string, definition experiment.Definition) (experimentsapi.StoredExperiment, error) {
	return store.update(ctx, teamID, id, revision, actor, definition)
}
func (store *experimentStore) Get(ctx context.Context, teamID int, id experiment.ID) (experimentsapi.StoredExperiment, bool, error) {
	return store.get(ctx, teamID, id)
}
func (store *experimentStore) List(ctx context.Context, teamID int) ([]experimentsapi.StoredExperiment, error) {
	return store.list(ctx, teamID)
}
func (store *experimentStore) ListPage(ctx context.Context, teamID int, filter experiment.ListFilter) ([]experimentsapi.StoredExperiment, error) {
	if store.listPage != nil {
		return store.listPage(ctx, teamID, filter)
	}
	return store.list(ctx, teamID)
}
func (store *experimentStore) PreflightStart(ctx context.Context, teamID int, id experiment.ID, revision int64) (experimentsapi.StoredExperiment, error) {
	return store.preflight(ctx, teamID, id, revision)
}
func (store *experimentStore) Start(ctx context.Context, teamID int, id experiment.ID, revision int64, actor string) (experimentsapi.StoredExperiment, error) {
	return store.start(ctx, teamID, id, revision, actor)
}
func (store *experimentStore) Cancel(ctx context.Context, teamID int, id experiment.ID, revision int64, actor string) (experimentsapi.StoredExperiment, error) {
	return store.cancel(ctx, teamID, id, revision, actor)
}
func (store *experimentStore) ListCells(ctx context.Context, teamID int, id experiment.ID) ([]experimentsapi.StoredCell, error) {
	return store.listCells(ctx, teamID, id)
}
func (store *experimentStore) GetCell(ctx context.Context, teamID int, id experiment.ID, cell experiment.CellID) (experimentsapi.StoredCell, bool, error) {
	return store.getCell(ctx, teamID, id, cell)
}
func (store *experimentStore) Scorecard(ctx context.Context, teamID int, id experiment.ID) (experiment.Scorecard, error) {
	return store.scorecard(ctx, teamID, id)
}

func TestHandlerCreatesAndUpdatesDraftsWithOptimisticRevision(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	var createdTeam int
	var createdTeamName, createdActor string
	store.create = func(_ context.Context, teamID int, teamName, actor string, got experiment.Definition) (experimentsapi.StoredExperiment, error) {
		createdTeam, createdTeamName, createdActor = teamID, teamName, actor
		if !definitionsEqual(got, definition) {
			t.Fatalf("create definition = %#v", got)
		}
		return storedExperiment(definition, 1), nil
	}
	handler := mustExperimentHandler(t, store)

	response := httptest.NewRecorder()
	handler.Create(response, experimentRequest(http.MethodPost, "", "", encodeJSON(t, definition)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status/body = %d/%s", response.Code, response.Body.String())
	}
	if createdTeam != 7 || createdTeamName != "main" || createdActor != "alice" {
		t.Fatalf("create authority = %d/%q/%q", createdTeam, createdTeamName, createdActor)
	}
	if response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create ETag = %q", response.Header().Get("ETag"))
	}

	definition.Name = "review-prompts-updated"
	store.update = func(_ context.Context, teamID int, id experiment.ID, revision int64, actor string, got experiment.Definition) (experimentsapi.StoredExperiment, error) {
		if teamID != 7 || id != largeExperimentID || revision != 1 || actor != "alice" || !definitionsEqual(got, definition) {
			t.Fatalf("update = team:%d id:%s revision:%d actor:%q definition:%#v", teamID, id.String(), revision, actor, got)
		}
		return storedExperiment(definition, 2), nil
	}
	response = httptest.NewRecorder()
	handler.Update(response, experimentRequest(http.MethodPut, largeExperimentID.String(), "", encodeJSON(t, map[string]any{
		"revision": 1, "definition": definition,
	})))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("update status/etag/body = %d/%q/%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	store.update = func(context.Context, int, experiment.ID, int64, string, experiment.Definition) (experimentsapi.StoredExperiment, error) {
		return experimentsapi.StoredExperiment{}, experimentsapi.ErrRevisionConflict
	}
	response = httptest.NewRecorder()
	handler.Update(response, experimentRequest(http.MethodPut, largeExperimentID.String(), "", encodeJSON(t, map[string]any{
		"revision": 1, "definition": definition,
	})))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerValidatesStartsAndCancelsOneTeamScopedExperiment(t *testing.T) {
	definition := validDefinition(t)
	definition.Budget = experiment.Budget{}
	store := defaultExperimentStore(t, definition)
	var transitions []string
	store.preflight = func(_ context.Context, teamID int, id experiment.ID, revision int64) (experimentsapi.StoredExperiment, error) {
		transitions = append(transitions, "preflight")
		if teamID != 7 || id != largeExperimentID || revision != 4 {
			t.Fatalf("preflight arguments = %d/%s/%d", teamID, id.String(), revision)
		}
		return storedExperiment(definition, 4), nil
	}
	store.start = func(_ context.Context, teamID int, id experiment.ID, revision int64, actor string) (experimentsapi.StoredExperiment, error) {
		transitions = append(transitions, "start")
		if teamID != 7 || id != largeExperimentID || revision != 4 || actor != "alice" {
			t.Fatalf("start arguments = %d/%s/%d/%q", teamID, id.String(), revision, actor)
		}
		definition.State = experiment.StateRunning
		return storedExperiment(definition, 5), nil
	}
	store.cancel = func(_ context.Context, teamID int, id experiment.ID, revision int64, actor string) (experimentsapi.StoredExperiment, error) {
		transitions = append(transitions, "cancel")
		if teamID != 7 || id != largeExperimentID || revision != 5 || actor != "alice" {
			t.Fatalf("cancel arguments = %d/%s/%d/%q", teamID, id.String(), revision, actor)
		}
		definition.State = experiment.StateCanceled
		return storedExperiment(definition, 6), nil
	}
	handler := mustExperimentHandler(t, store)

	response := httptest.NewRecorder()
	handler.Validate(response, experimentRequest(http.MethodPost, largeExperimentID.String(), "", `{"revision":4}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"expected_cells":20`) {
		t.Fatalf("validate status/body = %d/%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.Start(response, experimentRequest(http.MethodPost, largeExperimentID.String(), "", `{"revision":4}`))
	if response.Code != http.StatusOK {
		t.Fatalf("start status/body = %d/%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.Cancel(response, experimentRequest(http.MethodPost, largeExperimentID.String(), "", `{"revision":5}`))
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status/body = %d/%s", response.Code, response.Body.String())
	}
	if strings.Join(transitions, ",") != "preflight,start,cancel" {
		t.Fatalf("transitions = %v", transitions)
	}
}

func TestHandlerValidationFailsClosedForUnsupportedTokenBudgets(t *testing.T) {
	definition := validDefinition(t)
	definition.Budget.MaxTokensPerCell = 10_000
	store := defaultExperimentStore(t, definition)
	store.preflight = func(context.Context, int, experiment.ID, int64) (experimentsapi.StoredExperiment, error) {
		return experimentsapi.StoredExperiment{}, fmt.Errorf("%w: max_tokens_per_cell cannot be hard-enforced", experimentsapi.ErrInvalidDefinition)
	}
	handler := mustExperimentHandler(t, store)

	response := httptest.NewRecorder()
	handler.Validate(response, experimentRequest(http.MethodPost, largeExperimentID.String(), "", `{"revision":4}`))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "max_tokens_per_cell cannot be hard-enforced") {
		t.Fatalf("validate status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerValidationAndStartFailClosedWhenNoRunnerIsEnabled(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	var calls int
	store.preflight = func(context.Context, int, experiment.ID, int64) (experimentsapi.StoredExperiment, error) {
		calls++
		return storedExperiment(definition, 4), nil
	}
	store.start = func(context.Context, int, experiment.ID, int64, string) (experimentsapi.StoredExperiment, error) {
		calls++
		return storedExperiment(definition, 5), nil
	}
	handler, err := experimentsapi.NewHandler(experimentsapi.Config{
		TeamID: 7, TeamName: "main", Store: store,
		Identity:        func(*http.Request) (string, error) { return "alice", nil },
		RunnerAvailable: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, invoke := range []func(http.ResponseWriter, *http.Request){handler.Validate, handler.Start} {
		response := httptest.NewRecorder()
		invoke(response, experimentRequest(http.MethodPost, largeExperimentID.String(), "", `{"revision":4}`))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "runner is not enabled") {
			t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("disabled runner reached persistence %d times", calls)
	}
}

func TestHandlerListsDetailsCellsAndScorecardWithQuotedIDs(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	cell := experimentsapi.StoredCell{
		ID: 9007199254740995, ExperimentID: largeExperimentID,
		FixtureID: 9007199254740997, FixtureLabel: "fixture-a",
		VariantID: 9007199254740999, VariantLabel: "control",
		Repetition: 1, Status: experiment.CellValidMeasurement,
	}
	store.listCells = func(_ context.Context, teamID int, id experiment.ID) ([]experimentsapi.StoredCell, error) {
		if teamID != 7 || id != largeExperimentID {
			t.Fatalf("cell list scope = %d/%s", teamID, id.String())
		}
		return []experimentsapi.StoredCell{cell}, nil
	}
	store.getCell = func(_ context.Context, teamID int, id experiment.ID, cellID experiment.CellID) (experimentsapi.StoredCell, bool, error) {
		return cell, teamID == 7 && id == largeExperimentID && cellID == cell.ID, nil
	}
	store.scorecard = func(_ context.Context, teamID int, id experiment.ID) (experiment.Scorecard, error) {
		return experiment.Scorecard{
			ExperimentID: id, Control: "control", Variants: map[string]experiment.VariantScore{},
			Comparisons: map[string]map[string]experiment.PairedComparison{}, Cells: []experiment.CellResult{},
		}, nil
	}
	handler := mustExperimentHandler(t, store)

	for name, invoke := range map[string]func(*httptest.ResponseRecorder){
		"list": func(response *httptest.ResponseRecorder) {
			handler.List(response, experimentRequest(http.MethodGet, "", "", ""))
		},
		"detail": func(response *httptest.ResponseRecorder) {
			handler.Get(response, experimentRequest(http.MethodGet, largeExperimentID.String(), "", ""))
		},
		"cells": func(response *httptest.ResponseRecorder) {
			handler.ListCells(response, experimentRequest(http.MethodGet, largeExperimentID.String(), "", ""))
		},
		"cell": func(response *httptest.ResponseRecorder) {
			handler.GetCell(response, experimentRequest(http.MethodGet, largeExperimentID.String(), cell.ID.String(), ""))
		},
		"scorecard": func(response *httptest.ResponseRecorder) {
			handler.Scorecard(response, experimentRequest(http.MethodGet, largeExperimentID.String(), "", ""))
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			invoke(response)
			if response.Code != http.StatusOK {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
			if name != "list" && !strings.Contains(response.Body.String(), largeExperimentID.String()) {
				t.Fatalf("body does not carry quoted experiment ID: %s", response.Body.String())
			}
		})
	}

	response := httptest.NewRecorder()
	handler.Get(response, experimentRequest(http.MethodGet, largeExperimentID.String(), "", ""))
	if strings.Contains(response.Body.String(), `"id":9007199254740993`) || !strings.Contains(response.Body.String(), `"id":"9007199254740993"`) {
		t.Fatalf("detail ID wire format = %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.GetCell(response, experimentRequest(http.MethodGet, largeExperimentID.String(), cell.ID.String(), ""))
	for _, exactID := range []string{
		`"fixture_id":"9007199254740997"`,
		`"variant_id":"9007199254740999"`,
	} {
		if !strings.Contains(response.Body.String(), exactID) {
			t.Fatalf("cell durable ID wire format is not quoted: %s", response.Body.String())
		}
	}
}

func TestHandlerPaginatesExperimentHistoryWithAnExclusiveStableCursor(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	createdAt := time.Date(2026, 7, 22, 12, 0, 0, 123000, time.UTC)
	values := []experimentsapi.StoredExperiment{
		{ID: 103, TeamName: "main", Definition: definition, Revision: 1, CreatedBy: "alice", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 102, TeamName: "main", Definition: definition, Revision: 1, CreatedBy: "alice", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 101, TeamName: "main", Definition: definition, Revision: 1, CreatedBy: "alice", CreatedAt: createdAt, UpdatedAt: createdAt},
	}
	var calls int
	store.listPage = func(_ context.Context, teamID int, filter experiment.ListFilter) ([]experimentsapi.StoredExperiment, error) {
		if teamID != 7 || filter.Limit != 3 {
			t.Fatalf("page scope/limit = %d/%d", teamID, filter.Limit)
		}
		calls++
		if calls == 1 {
			if filter.Before != nil {
				t.Fatalf("first page cursor = %#v", filter.Before)
			}
			return values, nil
		}
		if filter.Before == nil || filter.Before.ID != 102 || !filter.Before.CreatedAt.Equal(createdAt) {
			t.Fatalf("second page cursor = %#v", filter.Before)
		}
		return values[2:], nil
	}
	handler := mustExperimentHandler(t, store)

	firstRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/experiments?limit=2", nil)
	first := httptest.NewRecorder()
	handler.List(first, firstRequest)
	if first.Code != http.StatusOK {
		t.Fatalf("first status/body = %d/%s", first.Code, first.Body.String())
	}
	var firstPage []experimentsapi.StoredExperiment
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != 103 || firstPage[1].ID != 102 {
		t.Fatalf("first page = %#v", firstPage)
	}
	cursor := first.Header().Get("X-Next-Cursor")
	if cursor == "" {
		t.Fatal("first page omitted next cursor")
	}
	if link := first.Header().Get("Link"); !strings.Contains(link, "limit=2") || !strings.Contains(link, `rel="next"`) {
		t.Fatalf("next link = %q", link)
	}

	secondRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/experiments?limit=2&cursor="+url.QueryEscape(cursor), nil)
	second := httptest.NewRecorder()
	handler.List(second, secondRequest)
	if second.Code != http.StatusOK || second.Header().Get("X-Next-Cursor") != "" {
		t.Fatalf("second status/cursor/body = %d/%q/%s", second.Code, second.Header().Get("X-Next-Cursor"), second.Body.String())
	}
	var secondPage []experimentsapi.StoredExperiment
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != 101 {
		t.Fatalf("second page = %#v", secondPage)
	}
}

func TestHandlerRejectsMalformedOrAmbiguousExperimentPageParameters(t *testing.T) {
	handler := mustExperimentHandler(t, defaultExperimentStore(t, validDefinition(t)))
	for _, query := range []string{
		"cursor=not-a-cursor",
		"cursor=a&cursor=b",
		"limit=0",
		"limit=1&limit=2",
		"limit=1;cursor=ignored",
		"state=completed",
		fmt.Sprintf("limit=%d", experiment.MaxListedExperiments+1),
	} {
		response := httptest.NewRecorder()
		handler.List(response, httptest.NewRequest(http.MethodGet, "/api/v1/agent/experiments?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q status/body = %d/%s", query, response.Code, response.Body.String())
		}
	}
}

func TestHandlerFailsClosedWhenAStoreReturnsMoreCellsThanAdmissionAllows(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	store.listCells = func(context.Context, int, experiment.ID) ([]experimentsapi.StoredCell, error) {
		return make([]experimentsapi.StoredCell, experiment.MaxMaterializedCells+1), nil
	}
	handler := mustExperimentHandler(t, store)
	response := httptest.NewRecorder()
	handler.ListCells(response, experimentRequest(http.MethodGet, largeExperimentID.String(), "", ""))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "too many cells") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerFailsClosedWhenAStoreReturnsMoreExperimentsThanTheIndexAllows(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	store.list = func(context.Context, int) ([]experimentsapi.StoredExperiment, error) {
		return make([]experimentsapi.StoredExperiment, experiment.MaxListedExperiments+2), nil
	}
	handler := mustExperimentHandler(t, store)
	response := httptest.NewRecorder()
	handler.List(response, experimentRequest(http.MethodGet, "", "", ""))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "too many experiments") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsInvalidFixtureContractsAndNumericSnapshotIDs(t *testing.T) {
	definition := validDefinition(t)
	store := defaultExperimentStore(t, definition)
	store.create = func(context.Context, int, string, string, experiment.Definition) (experimentsapi.StoredExperiment, error) {
		t.Fatal("invalid definition reached persistence")
		return experimentsapi.StoredExperiment{}, nil
	}
	handler := mustExperimentHandler(t, store)

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "numeric snapshot ID", body: strings.Replace(encodeJSON(t, definition), `"101"`, `101`, 1), want: "quoted"},
		{name: "normal assertions", body: func() string {
			copy := definition
			copy.Fixtures = append([]experiment.Fixture(nil), definition.Fixtures...)
			copy.Fixtures[0].Assertions = []experiment.Assertion{{Metric: "quality", Comparator: experiment.ComparatorLT, Thresholds: []float64{1}}}
			return encodeJSON(t, copy)
		}(), want: "fixtures[0]"},
		{name: "between arity", body: func() string {
			copy := definition
			copy.Fixtures = append([]experiment.Fixture(nil), definition.Fixtures...)
			copy.Fixtures[1].Assertions = []experiment.Assertion{{Metric: "quality", Comparator: experiment.ComparatorBetween, Thresholds: []float64{1}}}
			return encodeJSON(t, copy)
		}(), want: "fixtures[1]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.Create(response, experimentRequest(http.MethodPost, "", "", test.body))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("status/body = %d/%s, want %q", response.Code, response.Body.String(), test.want)
			}
		})
	}
}

func defaultExperimentStore(t *testing.T, definition experiment.Definition) *experimentStore {
	t.Helper()
	stored := storedExperiment(definition, 4)
	return &experimentStore{
		create: func(context.Context, int, string, string, experiment.Definition) (experimentsapi.StoredExperiment, error) {
			return stored, nil
		},
		update: func(context.Context, int, experiment.ID, int64, string, experiment.Definition) (experimentsapi.StoredExperiment, error) {
			return stored, nil
		},
		get: func(_ context.Context, teamID int, id experiment.ID) (experimentsapi.StoredExperiment, bool, error) {
			return stored, teamID == 7 && id == largeExperimentID, nil
		},
		list: func(_ context.Context, teamID int) ([]experimentsapi.StoredExperiment, error) {
			if teamID != 7 {
				t.Fatalf("list team = %d", teamID)
			}
			return []experimentsapi.StoredExperiment{stored}, nil
		},
		preflight: func(context.Context, int, experiment.ID, int64) (experimentsapi.StoredExperiment, error) {
			return stored, nil
		},
		start: func(context.Context, int, experiment.ID, int64, string) (experimentsapi.StoredExperiment, error) {
			return stored, nil
		},
		cancel: func(context.Context, int, experiment.ID, int64, string) (experimentsapi.StoredExperiment, error) {
			return stored, nil
		},
		listCells: func(context.Context, int, experiment.ID) ([]experimentsapi.StoredCell, error) {
			return []experimentsapi.StoredCell{}, nil
		},
		getCell: func(context.Context, int, experiment.ID, experiment.CellID) (experimentsapi.StoredCell, bool, error) {
			return experimentsapi.StoredCell{}, false, nil
		},
		scorecard: func(context.Context, int, experiment.ID) (experiment.Scorecard, error) {
			return experiment.Scorecard{}, errors.New("scorecard unavailable")
		},
	}
}

func mustExperimentHandler(t *testing.T, store experimentsapi.Store) *experimentsapi.Handler {
	t.Helper()
	handler, err := experimentsapi.NewHandler(experimentsapi.Config{
		TeamID: 7, TeamName: "main", Store: store,
		Identity:        func(*http.Request) (string, error) { return "alice", nil },
		RunnerAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func experimentRequest(method, experimentID, cellID, body string) *http.Request {
	request := httptest.NewRequest(method, "/api/v1/agent/experiments", strings.NewReader(body))
	request.URL.RawQuery = url.Values{
		":experiment_id": {experimentID},
		":cell_id":       {cellID},
	}.Encode()
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func storedExperiment(definition experiment.Definition, revision int64) experimentsapi.StoredExperiment {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	return experimentsapi.StoredExperiment{
		ID: largeExperimentID, TeamName: "main", Definition: definition,
		Revision: revision, CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
}

func validDefinition(t *testing.T) experiment.Definition {
	t.Helper()
	signature := workflow.PublicSignature{
		Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}},
		Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
	}
	hash, err := experiment.HashSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	return experiment.Definition{
		Name: "review-prompts", State: experiment.StateDraft, Signature: signature, Repetitions: 5,
		Budget: experiment.Budget{PerCellUSD: 1, TotalUSD: 20},
		Variants: []experiment.Variant{
			{Label: "control", Control: true, SignatureHash: hash, Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "review", DefinitionID: 41, Version: 3}},
			{Label: "candidate", SignatureHash: hash, Target: experiment.Target{Kind: experiment.TargetFunction, WorkflowName: "review", DefinitionID: 42, Version: 4, FunctionID: "review"}},
		},
		Fixtures: []experiment.Fixture{
			{Label: "normal", Role: experiment.FixtureNormal, Inputs: map[string]snapshot.SnapshotID{"repo": 101}},
			{Label: "negative", Role: experiment.FixtureNegativeControl, Inputs: map[string]snapshot.SnapshotID{"repo": 102}, Assertions: []experiment.Assertion{{Metric: "defects", Comparator: experiment.ComparatorGTE, Thresholds: []float64{1}}}},
		},
		Evaluator: experiment.Evaluator{
			Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "judge", DefinitionID: 51, Version: 2},
			Signature: workflow.PublicSignature{
				Inputs: []workflow.SignaturePort{
					{Name: "candidate", Type: "review/v1"}, {Name: "repo", Type: "repository/v1"},
				},
				Outputs: []workflow.SignaturePort{{Name: "measurements", Type: "measurements/v1"}},
			},
			Mappings: []experiment.EvaluatorMapping{
				{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
				{EvaluatorPort: "repo", SourceDirection: experiment.SourceFixtureInput, SourcePort: "repo"},
			},
			MeasurementsPort: "measurements",
		},
	}
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func definitionsEqual(left, right experiment.Definition) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
