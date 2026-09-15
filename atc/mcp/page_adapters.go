package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
)

func pageAdapter(api http.Handler, id string) mcpserver.ToolHandler {
	if id != "pipelines_list" && id != "builds_list" {
		return nil
	}
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		var a struct {
			pipelineArguments
			Query  string `json:"query"`
			Job    string `json:"job"`
			Status string `json:"status"`
			Limit  int    `json:"limit"`
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(input, &a)
		query := url.Values{"format": {"page"}}
		path := "/api/v1/pipelines"
		if a.Limit > 0 {
			query.Set("limit", strconv.Itoa(a.Limit))
		}
		if a.Cursor != "" {
			query.Set("cursor", a.Cursor)
		}
		if id == "pipelines_list" {
			query.Set("team", a.Team)
			query.Set("query", a.Query)
		} else {
			path = a.path() + "/builds"
			query.Set("job", a.Job)
			query.Set("status", a.Status)
			for key, values := range a.query() {
				query[key] = values
			}
		}
		r, err := apiRequest(ctx, api, "GET", path, query, nil, nil)
		if err != nil {
			return nil, err
		}
		if err = responseError(r); err != nil {
			return nil, err
		}
		items := []any{}
		var next *string
		if id == "pipelines_list" {
			var result atc.PipelinePage
			if json.Unmarshal(r.body.Bytes(), &result) != nil {
				return nil, errors.New("INVALID_RESULT: invalid pipeline page")
			}
			for _, p := range result.Items {
				items = append(items, pipelineView(p))
			}
			next = result.NextCursor
		} else {
			var result atc.BuildPage
			if json.Unmarshal(r.body.Bytes(), &result) != nil {
				return nil, errors.New("INVALID_RESULT: invalid build page")
			}
			for _, b := range result.Items {
				items = append(items, buildView(b))
			}
			next = result.NextCursor
		}
		return map[string]any{"items": items, "next_cursor": next}, nil
	}
}
