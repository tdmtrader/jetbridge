package jetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// DurableTierHeader is set by a daemon that has a durable tier configured. Its
// presence — on any response, at any status — is how the ATC learns the cluster
// can serve a warm at all.
//
// Capability rides an existing response rather than being probed for, so an ATC
// talking to daemons that predate the tier makes zero requests to a route they
// do not have.
const DurableTierHeader = "X-Durable-Tier"

// warmResponseHeaderTimeout bounds how long a daemon may take to say anything
// at all about a restore. The transfer itself is bounded by the caller's
// context, not this.
const warmResponseHeaderTimeout = 30 * time.Second

// warmAttempts is how many candidate daemons a warm will try.
//
// Two, not all of them: a 404 is the shared bucket's answer and asking a second
// pod re-asks the same bucket. The retry exists only for the case where the
// first candidate is unreachable — mid-roll, evicted — not to search.
const warmAttempts = 2

// WarmResourceCache asks a daemon to pull the object out of durable storage and
// register it locally, returning the IP of the daemon that now holds it.
//
// Unlike a probe, this makes its own answer true before returning, so the
// caller may bind to the result exactly as it would to a probe hit.
//
// Candidates are ranked by rendezvous hash so that concurrent builds wanting the
// same cache converge on the same node instead of each warming a private copy.
func (d *DaemonClient) WarmResourceCache(ctx context.Context, durableKey string, eps []daemonEndpoint) (string, bool) {
	logger := d.logger.Session("warm-resource-cache", lager.Data{"key": durableKey})

	if len(eps) == 0 {
		return "", false
	}

	body, err := json.Marshal(durableRestoreRequest{Key: durableKey})
	if err != nil {
		return "", false
	}

	candidates := warmOwners(durableKey, eps)
	if len(candidates) > warmAttempts {
		candidates = candidates[:warmAttempts]
	}

	for _, ep := range candidates {
		url := fmt.Sprintf("%s://%s:%d/durable/restore", d.scheme, ep.IP, d.port)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
		if err != nil {
			return "", false
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := d.warmClient.Do(req)
		if err != nil {
			// Transport failure says nothing about the object — the pod may be
			// rolling. The next candidate is worth a try.
			logger.Debug("daemon-unreachable", lager.Data{"ip": ep.IP, "error": err.Error()})
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			logger.Info("warmed", lager.Data{"ip": ep.IP, "node": ep.Node, "status": resp.StatusCode})
			return ep.IP, true
		}

		// Any other status is the bucket's answer, not this pod's. A 404 here
		// means the object is not in the store; the next pod would ask the same
		// store and get the same answer.
		logger.Debug("warm-declined", lager.Data{"ip": ep.IP, "status": resp.StatusCode})

		return "", false
	}

	return "", false
}

// warmOwners ranks daemons for a key by rendezvous (highest-random-weight)
// hash, so every ATC process independently picks the same daemon for the same
// key and concurrent builds share one warm rather than each pulling a copy.
//
// It hashes the NODE NAME, not the pod IP. A DaemonSet rolling update replaces
// every pod IP at once, which would void rendezvous hashing's only advantage
// over key%N for the single most common churn event in the cluster. Node names
// survive pod restarts, evictions and image upgrades.
//
// Endpoints without a node name fall to the back: they are still usable, but
// they cannot be agreed on by other ATC processes, so they are a last resort.
func warmOwners(key string, eps []daemonEndpoint) []daemonEndpoint {
	type ranked struct {
		ep     daemonEndpoint
		weight uint64
		named  bool
	}

	rankings := make([]ranked, 0, len(eps))
	for _, ep := range eps {
		identity := ep.Node
		if identity == "" {
			identity = ep.IP
		}

		sum := sha256.Sum256([]byte(key + "\x00" + identity))
		rankings = append(rankings, ranked{
			ep:     ep,
			weight: binary.BigEndian.Uint64(sum[:8]),
			named:  ep.Node != "",
		})
	}

	sort.Slice(rankings, func(i, j int) bool {
		if rankings[i].named != rankings[j].named {
			return rankings[i].named
		}
		if rankings[i].weight != rankings[j].weight {
			return rankings[i].weight > rankings[j].weight
		}

		// Total order even on a hash tie, so every ATC agrees.
		return rankings[i].ep.IP < rankings[j].ep.IP
	})

	owners := make([]daemonEndpoint, len(rankings))
	for i, r := range rankings {
		owners[i] = r.ep
	}

	return owners
}

// durableRestoreRequest is the body of POST /durable/restore.
//
// The key travels in the body rather than the path deliberately. As a path
// segment it would have to be un-escaped and joined onto the storage root,
// where "%2e%2e%2f%2e%2e%2fpwned" decodes to "../../pwned" and escapes the
// directory entirely. It also keeps this route consistent with /register,
// /resolve and /mirror, and avoids a path wildcard rejecting the multi-segment
// keys the daemon's own registry scan can produce.
type durableRestoreRequest struct {
	Key string `json:"key"`
}
