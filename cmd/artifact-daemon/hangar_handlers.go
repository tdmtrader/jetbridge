package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/hangar"
)

const maxHangarMaterializationItems = 128

var errDuplicateHangarGrant = errors.New("duplicate materialization grant")

type hangarMaterializationRequest struct {
	Items []hangarMaterializationItem `json:"items"`
}

type hangarMaterializationItem struct {
	Ref    hangar.TreeRef `json:"ref"`
	Handle string         `json:"handle"`
	Volume string         `json:"volume"`
	Grant  string         `json:"grant"`
}

func (s *Server) handleHangarPublish(w http.ResponseWriter, r *http.Request) {
	service := s.hangar
	if service == nil {
		http.NotFound(w, r)
		return
	}
	scope := hangar.Scope(r.PathValue("scope"))
	if err := scope.Validate(); err != nil {
		s.refuseHangarMalformed(w, r)
		return
	}
	if r.ContentLength > service.MaxArchiveBytes {
		s.refuseHangar(w, r, hangar.ErrLimitExceeded)
		return
	}
	body := http.MaxBytesReader(w, r.Body, service.MaxArchiveBytes)
	tree, err := service.Canonicalizer.Capture(r.Context(), body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, hangar.ErrLimitExceeded) {
			s.refuseHangar(w, r, hangar.ErrLimitExceeded)
		} else if r.Context().Err() != nil {
			s.refuseHangar(w, r, hangar.ErrInfrastructure)
		} else {
			s.refuseHangarMalformed(w, r)
		}
		return
	}
	defer tree.Close()

	archive, err := os.Open(tree.ArchivePath)
	if err != nil {
		s.refuseHangar(w, r, hangar.ErrInfrastructure)
		return
	}
	attributes, created, ensureErr := service.Store.EnsureTree(r.Context(), scope, tree.Digest, archive, service.MaxArchiveBytes)
	closeErr := archive.Close()
	if ensureErr != nil || closeErr != nil {
		if ensureErr == nil {
			ensureErr = hangar.ErrInfrastructure
		}
		s.refuseHangar(w, r, ensureErr)
		return
	}
	if attributes.Ref.Scope != scope || attributes.Ref.Digest != tree.Digest || attributes.Ref.Generation <= 0 || attributes.LogicalBytes != tree.ByteSize {
		s.refuseHangar(w, r, hangar.ErrInfrastructure)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(attributes)
}

func (s *Server) handleHangarMaterializations(w http.ResponseWriter, r *http.Request) {
	service := s.hangar
	if service == nil {
		http.NotFound(w, r)
		return
	}
	var request hangarMaterializationRequest
	if err := decodeHangarControl(w, r, service.MaxControlBytes, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.refuseHangar(w, r, hangar.ErrLimitExceeded)
		} else if errors.Is(err, errDuplicateHangarGrant) {
			s.refuseHangar(w, r, hangar.ErrUnauthorized)
		} else {
			s.refuseHangarMalformed(w, r)
		}
		return
	}
	if len(request.Items) == 0 || len(request.Items) > maxHangarMaterializationItems {
		if len(request.Items) > maxHangarMaterializationItems {
			s.refuseHangar(w, r, hangar.ErrLimitExceeded)
		} else {
			s.refuseHangarMalformed(w, r)
		}
		return
	}

	// This loop must finish for the entire batch before Materialize is called.
	// An invalid capability therefore cannot leave an authorized prefix visible.
	for _, item := range request.Items {
		token, ok := exactBearerGrant(item.Grant)
		if !ok || service.GrantVerifier == nil || service.GrantVerifier.Verify(token, item.Ref, item.Handle, item.Volume) != nil {
			s.refuseHangar(w, r, hangar.ErrUnauthorized)
			return
		}
	}
	// Bounded AFTER the batch is authorized and BEFORE any of it runs. After,
	// so an unauthenticated caller cannot occupy a slot or measure the node's
	// load; before, so the whole batch either runs or is refused, which is the
	// same all-or-nothing the authorization loop above establishes.
	//
	// A full channel refuses rather than waits — see hangarSem in server.go.
	// The 503 is the status the init container already retries, and it is the
	// same status and body every other Hangar infrastructure refusal returns,
	// so overload is not a distinguishable signal to an unauthenticated caller.
	select {
	case s.hangarSem <- struct{}{}:
	default:
		s.refuse(w, r, http.StatusServiceUnavailable, reasonOverloaded, errors.New("service unavailable"))
		return
	}
	defer func() { <-s.hangarSem }()

	for _, item := range request.Items {
		if err := service.Materializer.Materialize(r.Context(), item.Ref, item.Handle, item.Volume); err != nil {
			s.refuseHangar(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func exactBearerGrant(value string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(value, prefix)
	return token, token != "" && strings.TrimSpace(token) == token && !strings.ContainsAny(token, " \t\r\n,")
}

func decodeHangarControl(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	if limit <= 0 {
		return fmt.Errorf("invalid control limit")
	}
	if r.ContentLength > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	bounded := http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(bounded)
	if err != nil {
		return err
	}
	if err := validateHangarControlSchema(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain exactly one JSON value")
	}
	return nil
}

func validateHangarControlSchema(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var parseObject func(map[string]func() error) error
	parseScalar := func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if _, composite := token.(json.Delim); composite {
			return fmt.Errorf("expected scalar JSON value")
		}
		return nil
	}
	parseObject = func(fields map[string]func() error) error {
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return fmt.Errorf("expected JSON object")
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			parse, allowed := fields[key]
			if !allowed {
				return fmt.Errorf("unknown or noncanonical JSON field")
			}
			if _, duplicate := seen[key]; duplicate {
				if key == "grant" {
					return errDuplicateHangarGrant
				}
				return fmt.Errorf("duplicate JSON field")
			}
			seen[key] = struct{}{}
			if err := parse(); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
		return nil
	}
	parseRef := func() error {
		return parseObject(map[string]func() error{"scope": parseScalar, "digest": parseScalar, "generation": parseScalar})
	}
	parseItem := func() error {
		return parseObject(map[string]func() error{"ref": parseRef, "handle": parseScalar, "volume": parseScalar, "grant": parseScalar})
	}
	parseItems := func() error {
		start, err := decoder.Token()
		if err != nil || start != json.Delim('[') {
			return fmt.Errorf("expected items array")
		}
		for decoder.More() {
			if err := parseItem(); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid items array")
		}
		return nil
	}
	return parseObject(map[string]func() error{"items": parseItems})
}

// Hangar replies go through s.refuse for the same reason every other daemon
// refusal does: a refused build must leave a counted, logged trace instead of
// a bare http.Error. Status and client-visible message are unchanged from the
// standalone writers these replaced; only the accounting is new, and the case
// order is preserved exactly (ErrInfrastructure first, so a wrapped
// infrastructure failure is never reported as a client fault).
//
// The reply text stays a fixed classification and is never err.Error(): a
// Hangar error can carry a scope, a digest or a store message, and refuse
// writes its error text to the client.
func (s *Server) refuseHangarMalformed(w http.ResponseWriter, r *http.Request) {
	s.refuse(w, r, http.StatusBadRequest, reasonMalformed, errors.New("malformed request"))
}

func (s *Server) refuseHangar(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	// First, so a wrapped infrastructure failure is never reported as a client
	// fault. It is also the one branch that is not a refusal at all.
	case errors.Is(err, hangar.ErrInfrastructure), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.hangarUnavailable(w, r)
	case errors.Is(err, hangar.ErrUnauthorized):
		s.refuse(w, r, http.StatusUnauthorized, reasonCapability, errors.New("unauthorized"))
	case errors.Is(err, hangar.ErrCorrupt):
		s.refuse(w, r, http.StatusUnprocessableEntity, reasonTreeVerification, errors.New("tree verification failed"))
	case errors.Is(err, hangar.ErrLimitExceeded):
		s.refuse(w, r, http.StatusRequestEntityTooLarge, reasonLimitExceeded, errors.New("request too large"))
	case errors.Is(err, hangar.ErrConflict):
		s.refuse(w, r, http.StatusConflict, reasonConflict, errors.New("conflict"))
	case errors.Is(err, hangar.ErrNotFound):
		s.refuse(w, r, http.StatusNotFound, reasonNotFound, errors.New("not found"))
	default:
		// Unclassified. Still ours: an error the store did not label is a
		// daemon-side fault until someone proves otherwise, and reporting it
		// as a client fault is how a bucket outage gets read as bad pipelines.
		s.hangarUnavailable(w, r)
	}
}

// hangarUnavailable answers the daemon's own failures.
//
// artifact_daemon_refusals_total answers one question: how often did the daemon
// turn a client away for something the CLIENT did. A bucket that will not
// answer, a request whose context was cancelled or timed out, and an error the
// store did not classify are none of those — routing them through s.refuse made
// one metric mean two things, so a Hangar outage looked like a wave of bad
// requests, and the malformed-request rate an operator actually wants to alert
// on could not be read at all.
//
// Core already draws this line: handleDurableRestore writes its normal-outcome
// 404 with http.Error rather than s.refuse, and is listed in
// refusal_visibility_test.go's `known` map for exactly that reason. This is the
// second entry there.
//
// Status and body are unchanged — a fixed classification, never err.Error(),
// because a Hangar error can carry a scope, a digest or a store message. The
// event is still logged, with the same bounded route label the refusal path
// uses; only the counter, and the word "refused", are withdrawn.
func (s *Server) hangarUnavailable(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("hangar-unavailable", lager.Data{
		"route":  refusalRoute(r),
		"status": http.StatusServiceUnavailable,
	})
	http.Error(w, "service unavailable", http.StatusServiceUnavailable)
}
