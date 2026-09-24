// Package diskserver serves fixed, namespace-scoped roles over authenticated
// HTTP. It has no policy mutation, overwrite, or unconditional delete route.
package diskserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/internal/disktransport"
	"github.com/concourse/concourse/hangar/objectstore"
)

type Config struct {
	StoreID         string
	InputNamespace  string
	OutputNamespace string
	Credentials     map[string]string
	MaxConcurrent   int
}

type credential struct {
	role string
	hash [32]byte
}
type server struct {
	store       *disk.Store
	config      Config
	credentials []credential
	slots       chan struct{}
}

func New(store *disk.Store, config Config) (http.Handler, error) {
	if store == nil || config.MaxConcurrent <= 0 {
		return nil, fmt.Errorf("disk server requires a store and positive concurrency")
	}
	if store.ID() != config.StoreID {
		return nil, fmt.Errorf("%w: disk server identity differs from its index", hangar.ErrConflict)
	}
	for _, name := range []string{config.StoreID, config.InputNamespace, config.OutputNamespace} {
		if err := hangar.Scope(name).Validate(); err != nil {
			return nil, err
		}
	}
	if config.InputNamespace == config.OutputNamespace {
		return nil, fmt.Errorf("input and output namespaces must differ")
	}
	s := &server{store: store, config: config, slots: make(chan struct{}, config.MaxConcurrent)}
	seen := map[string]bool{}
	for _, role := range []string{"input", "publisher", "inventory", "reclaimer"} {
		token := strings.TrimSpace(config.Credentials[role])
		if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") || seen[token] {
			return nil, fmt.Errorf("each disk role requires a distinct 32..4096 byte credential")
		}
		seen[token] = true
		s.credentials = append(s.credentials, credential{role: role, hash: sha256.Sum256([]byte(token))})
	}
	if len(config.Credentials) != 4 {
		return nil, fmt.Errorf("unrecognized disk role")
	}
	// Keep only digests after initialization.
	s.config.Credentials = nil
	return s, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(disktransport.IdentityHeader, s.config.StoreID)
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	role := ""
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(token))
	for _, credential := range s.credentials {
		if subtle.ConstantTimeCompare(hash[:], credential.hash[:]) == 1 {
			role = credential.role
		}
	}
	if role == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		fail(w, objectstore.ErrUnauthorized)
		return
	}
	if r.Header.Get(disktransport.IdentityHeader) != s.config.StoreID {
		fail(w, hangar.ErrConflict)
		return
	}
	if r.URL.Path == "/v1/identity" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	query := r.URL.Query()
	bucket := query.Get("bucket")
	key := query.Get("key")
	operation := strings.TrimPrefix(r.URL.Path, "/v1/")
	allowed := false
	if role == "input" && bucket == s.config.InputNamespace {
		allowed = operation == "create" || operation == "stat" || operation == "read"
	}
	if bucket == s.config.OutputNamespace {
		switch role {
		case "publisher":
			allowed = operation == "create" || operation == "stat" || operation == "read"
		case "inventory":
			allowed = operation == "stat" || operation == "list"
		case "reclaimer":
			allowed = operation == "stat" || operation == "delete"
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") || !allowed {
		fail(w, objectstore.ErrUnauthorized)
		return
	}
	method := http.MethodGet
	if operation == "create" {
		method = http.MethodPost
	}
	if operation == "delete" {
		method = http.MethodDelete
	}
	if r.Method != method {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, objectstore.ErrInfrastructure)
		return
	}
	generation := int64(0)
	if raw := query.Get("generation"); raw != "" {
		var err error
		generation, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || generation < 0 {
			fail(w, objectstore.ErrPreconditionFailed)
			return
		}
	}
	ctx := r.Context()
	switch operation {
	case "create":
		encoded := r.Header.Get("X-Hangar-Metadata")
		if len(encoded) > 90<<10 {
			fail(w, hangar.ErrLimitExceeded)
			return
		}
		metadata := map[string]string{}
		if encoded != "" {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || json.Unmarshal(raw, &metadata) != nil {
				fail(w, hangar.ErrCorrupt)
				return
			}
		}
		attrs, err := s.store.CreateAbsent(ctx, bucket, key, metadata, r.Body)
		respond(w, attrs, err)
	case "stat":
		var attrs objectstore.Attrs
		var err error
		if generation == 0 {
			attrs, err = s.store.StatCurrent(ctx, bucket, key)
		} else {
			attrs, err = s.store.StatExact(ctx, bucket, key, generation)
		}
		respond(w, attrs, err)
	case "read":
		body, err := s.store.OpenExact(ctx, bucket, key, generation)
		if err != nil {
			fail(w, err)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Trailer", disktransport.ErrorTrailer)
		w.WriteHeader(http.StatusOK)
		_, err = io.Copy(w, body)
		code := "ok"
		if err != nil {
			_, code = disktransport.ErrorCode(err)
		}
		w.Header().Set(disktransport.ErrorTrailer, code)
	case "delete":
		if err := s.store.DeleteExact(ctx, bucket, key, generation); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "list":
		pageSize, err := strconv.Atoi(query.Get("page_size"))
		if err != nil || pageSize <= 0 || pageSize > 1000 {
			fail(w, hangar.ErrLimitExceeded)
			return
		}
		// Each accepted object carries at most 64 KiB of encoded metadata and
		// a 1 KiB key (up to 6 KiB escaped). A shorter page keeps even worst-case
		// objects below the 8 MiB control response limit while advancing the cursor.
		pageSize = min(pageSize, 100)
		afterGeneration, err := strconv.ParseInt(query.Get("after_generation"), 10, 64)
		if err != nil || afterGeneration < 0 {
			fail(w, objectstore.ErrPreconditionFailed)
			return
		}
		page, err := s.store.List(ctx, bucket, objectstore.ListRequest{Prefix: query.Get("prefix"), After: query.Get("after"), AfterGeneration: afterGeneration, PageSize: pageSize})
		respond(w, page, err)
	}
}

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		fail(w, err)
		return
	}
	if len(body) > disktransport.MaxControlBytes {
		fail(w, hangar.ErrLimitExceeded)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
func fail(w http.ResponseWriter, err error) {
	status, code := disktransport.ErrorCode(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
