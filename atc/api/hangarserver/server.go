// Package hangarserver is the admin API over the Hangar output plane's status:
// GET /api/v1/hangar/status and PUT /api/v1/hangar/findings/:finding_id/resolve.
package hangarserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// Source reads the status and resolves a finding. Nil is a web node with no
// output plane configured.
type Source interface {
	Read(ctx context.Context) (hangaroutput.Status, error)
	Resolve(ctx context.Context, id int64) error
}

type Server struct {
	logger lager.Logger
	source Source
}

func NewServer(logger lager.Logger, source Source) *Server {
	return &Server{logger: logger, source: source}
}

func (s *Server) unconfigured(w http.ResponseWriter) bool {
	if s.source != nil {
		return false
	}
	http.Error(w, "this web node has no Hangar output plane configured", http.StatusNotFound)
	return true
}

func (s *Server) GetStatus(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("hangar-status")
	if s.unconfigured(w) {
		return
	}

	status, err := s.source.Read(r.Context())
	if err != nil {
		logger.Error("failed-to-read", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(Present(status)); err != nil {
		logger.Error("failed-to-encode", err)
	}
}

func (s *Server) ResolveFinding(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("hangar-resolve-finding")
	if s.unconfigured(w) {
		return
	}

	id, err := strconv.ParseInt(r.FormValue(":finding_id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "a finding id is a positive integer", http.StatusBadRequest)
		return
	}

	err = s.source.Resolve(r.Context(), id)
	switch {
	case err == nil:
		logger.Info("resolved", lager.Data{"finding": id})
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, output.ErrNotFound):
		http.Error(w, "no open integrity finding with that id", http.StatusNotFound)
	default:
		logger.Error("failed-to-resolve", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// Present is the wire form of one status read.
func Present(status hangaroutput.Status) atc.HangarStatus {
	counts := status.Counts
	presented := atc.HangarStatus{
		Enabled:         status.Enabled,
		AtRisk:          status.AtRisk,
		Drained:         status.Drained(),
		LiveGenerations: counts.LiveGenerations,
		Residue: atc.HangarResidue{
			PendingCaptures:        counts.PendingCaptures,
			PublishingCaptures:     counts.PublishingCaptures,
			UnreleasedCaptures:     counts.UnreleasedCaptures,
			OpenClaims:             counts.OpenClaims,
			LiveReadLeases:         counts.OpenReadLeases,
			UnfinalizedReclaimJobs: counts.UnfinalizedReclaimJobs,
			Total:                  counts.Residue(),
			UnacknowledgedReleases: counts.UnacknowledgedReleases,
		},
		Findings: []atc.HangarFinding{},
	}
	for _, finding := range status.Findings {
		presented.Findings = append(presented.Findings, atc.HangarFinding{
			ID:              finding.ID,
			Violation:       string(finding.Violation),
			Subject:         finding.Subject,
			Detail:          finding.Detail,
			ObservedAt:      finding.ObservedAt.Unix(),
			BlocksAdmission: finding.BlocksAdmission,
		})
	}

	return presented
}
