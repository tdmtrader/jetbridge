package steps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// scanImages owns real registries and expected image identities. These maps
// choose fixture inputs and assertions; no resolver reads them to answer a
// request. The scanner gets imageresolver.NewResolver directly.
type scanImages struct {
	public    *ociRegistry
	origins   map[string]*ociRegistry
	digests   map[string]string
	transport *http.Transport
	recorder  *brine.Recorder
}

func newScanImages(rec *brine.Recorder) (*scanImages, error) {
	public, err := newOCIRegistry("", "")
	if err != nil {
		return nil, err
	}
	rec.RegisterDisposer(func() {
		if err := public.close(); err != nil {
			panic(err)
		}
	})
	transport := public.server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
	rec.RegisterDisposer(transport.CloseIdleConnections)
	return &scanImages{public: public, origins: map[string]*ociRegistry{}, digests: map[string]string{}, transport: transport, recorder: rec}, nil
}

func (r *scanImages) resolver() imageresolver.Resolver {
	return imageresolver.NewResolver(authn.NewMultiKeychain(), remote.WithTransport(r.transport))
}

func (r *scanImages) publish(ref, identity, username, password string) error {
	repository, tag := splitRef(ref)
	if tag == "" {
		tag = "latest"
	}
	origin := r.origins[repository]
	if username != "" {
		if origin != nil {
			return fmt.Errorf("configure credentials before publishing repository %q", repository)
		}
		var err error
		origin, err = newOCIRegistry(username, password)
		if err != nil {
			return err
		}
		r.recorder.RegisterDisposer(func() {
			if err := origin.close(); err != nil {
				panic(err)
			}
		})
		// Given steps finish before the scanner starts concurrent resolution.
		r.transport.TLSClientConfig.RootCAs.AddCert(origin.server.Certificate())
	}
	if origin == nil {
		origin = r.public
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	digest, err := origin.publish(ctx, repository, tag, identity)
	if err != nil {
		return err
	}
	if previous, exists := r.digests[identity]; exists && previous != digest {
		return fmt.Errorf("image identity %q changed its manifest digest", identity)
	}
	r.origins[repository], r.digests[identity] = origin, digest
	return nil
}

func (r *scanImages) repository(ref string) string {
	repository, _ := splitRef(ref)
	if repository == "" {
		return ""
	}
	origin := r.origins[repository]
	if origin == nil {
		origin = r.public
	}
	return origin.host() + "/" + repository
}

// Preserve an omitted tag: only the production resolver supplies latest.
func (r *scanImages) source(ref string) atc.Source {
	_, tag := splitRef(ref)
	source := atc.Source{"repository": r.repository(ref)}
	if tag != "" {
		source["tag"] = tag
	}
	return source
}

func (r *scanImages) expectedDigest(identity string) (string, error) {
	digest, ok := r.digests[identity]
	if !ok {
		return "", fmt.Errorf("image %q has not been published", identity)
	}
	return digest, nil
}

func resolvedScanImage(pattern string, get func(ScanDone, string) (string, error)) brine.StepDefinition {
	return Assert[ScanDone](pattern, func(in ScanDone, a Args) error {
		actual, err := get(in, a.String(0))
		if err != nil {
			return err
		}
		expected, err := in.Ready.Registry.expectedDigest(a.String(1))
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("expected image %q digest %q, got %q", a.String(1), expected, actual)
		}
		return nil
	})
}

// Explicitly approved fault injection, installed only by the panic-isolation
// step to exercise recovery from an actual resolver panic. An HTTP server error
// cannot exercise that boundary. Ordinary resolution/authentication always uses
// the real resolver; this exception does not authorize other substitutes.
type panicImageResolver struct {
	next imageresolver.Resolver
	ref  string
}

func (r panicImageResolver) Resolve(ctx context.Context, repository, tag string, auth *imageresolver.BasicAuth) (string, error) {
	if canonicalRef(repository+":"+tag) == r.ref {
		panic("injected resolver panic for " + r.ref)
	}
	return r.next.Resolve(ctx, repository, tag, auth)
}
