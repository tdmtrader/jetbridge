package pipelineserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) pipelinePage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	team, text := query.Get("team"), query.Get("query")
	if len(team) > 256 || len(text) > 256 || !utf8.ValidString(text) {
		helpers.HandleBadRequest(w, "invalid pipeline filter")
		return
	}
	limit, err := helpers.PageLimit(query)
	if err != nil {
		helpers.HandleBadRequest(w, err.Error())
		return
	}
	key := queryKey("pipelines", team, text)
	after, err := helpers.PagePosition(query.Get("cursor"), key)
	if err != nil {
		helpers.HandleBadRequest(w, err.Error())
		return
	}
	acc := accessor.GetAccessor(r)
	pipelines, err := s.pipelineFactory.PipelinePage(acc.TeamNames(), acc.IsAdmin(), team, text, after, limit+1)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var next *string
	if len(pipelines) > limit {
		cursor := helpers.PageCursor(pipelines[limit-1].ID(), key)
		next = &cursor
		pipelines = pipelines[:limit]
	}
	items := present.Pipelines(pipelines, present.PipelinesOptions{OptionsForPipeline: func(p db.Pipeline) present.PipelineOptions { return pipelineOptions(r, acc, p) }})
	if items == nil {
		items = []atc.Pipeline{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(atc.PipelinePage{Items: items, NextCursor: next})
}

func (s *Server) buildPage(w http.ResponseWriter, r *http.Request, pipeline db.Pipeline) {
	query := r.URL.Query()
	job, status := query.Get("job"), query.Get("status")
	if len(job) > 256 {
		helpers.HandleBadRequest(w, "invalid job filter")
		return
	}
	switch atc.BuildStatus(status) {
	case "", atc.StatusPending, atc.StatusStarted, atc.StatusSucceeded, atc.StatusFailed, atc.StatusErrored, atc.StatusAborted:
	default:
		helpers.HandleBadRequest(w, "invalid build status")
		return
	}
	limit, err := helpers.PageLimit(query)
	if err != nil {
		helpers.HandleBadRequest(w, err.Error())
		return
	}
	key := queryKey("pipeline-builds-id", strconv.Itoa(pipeline.ID()), job, status)
	position, err := helpers.PagePosition(query.Get("cursor"), key)
	if err != nil {
		helpers.HandleBadRequest(w, err.Error())
		return
	}
	page := db.Page{Limit: limit}
	if position > 0 {
		page.To = &position
	}
	builds, pagination, err := pipeline.BuildsFiltered(job, atc.BuildStatus(status), page)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	result := atc.BuildPage{Items: make([]atc.Build, 0, len(builds))}
	for _, build := range builds {
		result.Items = append(result.Items, present.Build(build, nil, nil))
	}
	if pagination.Older != nil {
		cursor := helpers.PageCursor(*pagination.Older.To, key)
		result.NextCursor = &cursor
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
func queryKey(parts ...string) string { encoded, _ := json.Marshal(parts); return string(encoded) }
