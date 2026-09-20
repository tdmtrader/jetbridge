package jetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactwire"
)

// DurableTierHeader is the wire module's header, named here as well because
// the brine steps, a separate Go module, read it off the jetbridge package.
const DurableTierHeader = artifactwire.DurableTierHeader

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
func (d *DaemonClient) WarmResourceCache(ctx context.Context, cacheKey, durableKey string, eps []daemonEndpoint) (string, bool) {
	logger := d.logger.Session("warm-resource-cache", lager.Data{"key": durableKey})

	if len(eps) == 0 {
		return "", false
	}

	request := artifactwire.DurableRestoreRequest{Key: cacheKey, DurableKey: durableKey}

	candidates := warmOwners(durableKey, eps)
	if len(candidates) > warmAttempts {
		candidates = candidates[:warmAttempts]
	}

	for _, ep := range candidates {
		err := d.wire.Restore(ctx, ep.IP, request)
		if err == nil {
			logger.Info("warmed", lager.Data{"ip": ep.IP, "node": ep.Node})
			return ep.IP, true
		}

		var refusal *artifactwire.Refusal
		if !errors.As(err, &refusal) {
			// Transport failure says nothing about the object: the pod may be
			// rolling. The next candidate is worth a try.
			logger.Debug("daemon-unreachable", lager.Data{"ip": ep.IP, "error": err.Error()})
			continue
		}

		// Any other status is the bucket's answer, not this pod's. A 404 here
		// means the object is not in the store; the next pod would ask the same
		// store and get the same answer.
		logger.Debug("warm-declined", lager.Data{"ip": ep.IP, "status": refusal.Status})

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
