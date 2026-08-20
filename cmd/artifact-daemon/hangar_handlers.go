package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

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

func (s *Server) handleHangarOpen(w http.ResponseWriter, r *http.Request) {
	service := s.hangar
	if service == nil {
		http.NotFound(w, r)
		return
	}
	ref, err := hangarRefFromRequest(r)
	if err != nil {
		s.refuseHangarMalformed(w, r)
		return
	}
	reader, attributes, err := service.Store.OpenTree(r.Context(), ref, service.MaxArchiveBytes)
	if err != nil {
		s.refuseHangar(w, r, err)
		return
	}
	if reader == nil {
		s.refuseHangar(w, r, hangar.ErrCorrupt)
		return
	}

	spool, err := os.CreateTemp(service.Canonicalizer.TempDir, "hangar-response-*.tar")
	if err != nil {
		_ = reader.Close()
		s.refuseHangar(w, r, hangar.ErrInfrastructure)
		return
	}
	spoolName := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolName)
	}()
	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(spool, hasher), io.LimitReader(reader, service.MaxArchiveBytes+1))
	closeErr := reader.Close()
	if copyErr != nil {
		if hangarTypedError(copyErr) {
			s.refuseHangar(w, r, copyErr)
		} else {
			s.refuseHangar(w, r, hangar.ErrInfrastructure)
		}
		return
	}
	if closeErr != nil {
		if hangarTypedError(closeErr) {
			s.refuseHangar(w, r, closeErr)
		} else {
			s.refuseHangar(w, r, hangar.ErrInfrastructure)
		}
		return
	}
	if n > service.MaxArchiveBytes {
		s.refuseHangar(w, r, hangar.ErrLimitExceeded)
		return
	}
	actualDigest := hangar.Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if attributes.Ref != ref || attributes.LogicalBytes != n || actualDigest != ref.Digest {
		s.refuseHangar(w, r, hangar.ErrCorrupt)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		s.refuseHangar(w, r, hangar.ErrInfrastructure)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, spool); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func hangarRefFromRequest(r *http.Request) (hangar.TreeRef, error) {
	generationText := r.PathValue("generation")
	generation, err := strconv.ParseInt(generationText, 10, 64)
	if err != nil || strconv.FormatInt(generation, 10) != generationText {
		return hangar.TreeRef{}, fmt.Errorf("invalid generation")
	}
	return hangar.NewTreeRef(hangar.Scope(r.PathValue("scope")), hangar.Digest("sha256:"+r.PathValue("digest")), generation)
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
	status := http.StatusServiceUnavailable
	message := "service unavailable"
	reason := reasonUnavailable
	switch {
	case errors.Is(err, hangar.ErrInfrastructure), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, message, reason = http.StatusServiceUnavailable, "service unavailable", reasonUnavailable
	case errors.Is(err, hangar.ErrUnauthorized):
		status, message, reason = http.StatusUnauthorized, "unauthorized", reasonCapability
	case errors.Is(err, hangar.ErrCorrupt):
		status, message, reason = http.StatusUnprocessableEntity, "tree verification failed", reasonTreeVerification
	case errors.Is(err, hangar.ErrLimitExceeded):
		status, message, reason = http.StatusRequestEntityTooLarge, "request too large", reasonLimitExceeded
	case errors.Is(err, hangar.ErrConflict):
		status, message, reason = http.StatusConflict, "conflict", reasonConflict
	case errors.Is(err, hangar.ErrNotFound):
		status, message, reason = http.StatusNotFound, "not found", reasonNotFound
	}
	s.refuse(w, r, status, reason, errors.New(message))
}

func hangarTypedError(err error) bool {
	return errors.Is(err, hangar.ErrInfrastructure) || errors.Is(err, hangar.ErrUnauthorized) ||
		errors.Is(err, hangar.ErrCorrupt) || errors.Is(err, hangar.ErrLimitExceeded) ||
		errors.Is(err, hangar.ErrConflict) || errors.Is(err, hangar.ErrNotFound) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
