package steps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/atc/imageresolver"
	dcontext "github.com/docker/distribution/context"
	"github.com/docker/distribution/registry/auth"
	_ "github.com/docker/distribution/registry/auth/htpasswd"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/crypto/bcrypt"
)

// ociRegistry serves the OCI registry implementation over real TLS. It neither
// implements imageresolver.Resolver nor fabricates digests or auth responses.
// Private registries use Distribution's htpasswd access controller; tests never
// implement their own credential comparison. Everything is loopback and owned.
type ociRegistry struct {
	server      *httptest.Server
	root        string
	credentials authn.Authenticator
}

func newOCIRegistry(username, password string) (_ *ociRegistry, err error) {
	if (username == "") != (password == "") || strings.ContainsAny(username, ":\r\n") {
		return nil, fmt.Errorf("registry credentials require a username and password, or neither")
	}
	r := &ociRegistry{credentials: authn.Anonymous}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	var handler http.Handler = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	if username != "" {
		r.root, err = AttributedTempDir("brine-registry-auth-")
		if err != nil {
			return nil, err
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if hashErr != nil {
			return nil, hashErr
		}
		path := filepath.Join(r.root, "htpasswd")
		if err = os.WriteFile(path, []byte(username+":"+string(hash)+"\n"), 0600); err != nil {
			return nil, err
		}
		controller, authErr := auth.GetAccessController("htpasswd", map[string]interface{}{"realm": "brine-owned-registry", "path": path})
		if authErr != nil {
			return nil, authErr
		}
		next := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			ctx, authErr := controller.Authorized(dcontext.WithRequest(request.Context(), request))
			if authErr != nil {
				var challenge auth.Challenge
				if errors.As(authErr, &challenge) {
					challenge.SetHeaders(request, w)
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					http.Error(w, "registry authentication unavailable", http.StatusInternalServerError)
				}
				return
			}
			next.ServeHTTP(w, request.WithContext(ctx))
		})
		r.credentials = &authn.Basic{Username: username, Password: password}
	}
	r.server = httptest.NewTLSServer(handler)
	return r, nil
}

func (r *ociRegistry) host() string { return strings.TrimPrefix(r.server.URL, "https://") }

// A real production resolver, with trust scoped to this owned registry's test
// certificate. The empty production keychain avoids reading host credentials.
func (r *ociRegistry) resolver() imageresolver.Resolver {
	return imageresolver.NewResolver(authn.NewMultiKeychain(), remote.WithTransport(r.server.Client().Transport))
}

// publish gives the image distinct configuration bytes. The expected digest
// is hashed from those bytes' actual manifest before publication, not invented
// by a resolver double or read from the response under test.
func (r *ociRegistry) publish(ctx context.Context, repository, tag, identity string) (string, error) {
	if repository == "" || strings.Contains(repository, ":") || strings.Contains(repository, "@") || strings.HasPrefix(repository, "/") {
		return "", fmt.Errorf("expected a repository path inside the owned registry, got %q", repository)
	}
	ref, err := name.NewTag(r.host()+"/"+repository+":"+tag, name.StrictValidation)
	if err != nil {
		return "", err
	}
	config, err := empty.Image.ConfigFile()
	if err != nil {
		return "", err
	}
	config.Config.Labels = map[string]string{"brine.image.identity": identity}
	img, err := mutate.ConfigFile(empty.Image, config)
	if err != nil {
		return "", err
	}
	manifest, err := img.RawManifest()
	if err != nil {
		return "", err
	}
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	options := []remote.Option{remote.WithContext(ctx), remote.WithTransport(r.server.Client().Transport), remote.WithAuth(r.credentials)}
	if err := remote.Write(ref, img, options...); err != nil {
		return "", fmt.Errorf("publish OCI image: %w", err)
	}
	descriptor, err := remote.Get(ref, options...)
	if err != nil {
		return "", fmt.Errorf("read published OCI manifest: %w", err)
	}
	if !bytes.Equal(descriptor.Manifest, manifest) || descriptor.Digest.String() != expected {
		return "", fmt.Errorf("registry did not preserve the published manifest and digest")
	}
	return expected, nil
}

func (r *ociRegistry) close() error {
	if r.server != nil {
		r.server.CloseClientConnections()
		r.server.Close()
	}
	if r.root != "" {
		return os.RemoveAll(r.root)
	}
	return nil
}
