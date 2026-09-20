package steps

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// This integration check proves the replacement fixture's real HTTP, content,
// authentication and cleanup boundaries before scanner scenarios adopt it.
func TestOCIRegistryPublicationAuthenticationAndCleanup(t *testing.T) {
	for _, private := range []bool{false, true} {
		label := "public"
		username, password := "", ""
		if private {
			label, username, password = "private", "fixture-user", "fixture-password"
		}
		t.Run(label, func(t *testing.T) {
			r, err := newOCIRegistry(username, password)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			first, err := r.publish(ctx, "team/app", "latest", "first")
			if err != nil {
				t.Fatal(err)
			}
			second, err := r.publish(ctx, "team/other", "v2", "second")
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("distinct images have indistinguishable digests")
			}
			resolver := r.resolver()
			var credentials *imageresolver.BasicAuth
			if private {
				credentials = &imageresolver.BasicAuth{Username: username, Password: password}
			}
			check := func(repository, tag, expected string) {
				t.Helper()
				digest, err := resolver.Resolve(ctx, r.host()+"/"+repository, tag, credentials)
				if err != nil || digest != expected {
					t.Fatalf("resolve %s:%s got %q, %v; want %q", repository, tag, digest, err, expected)
				}
			}
			check("team/app", "", first)
			check("team/other", "v2", second)
			for _, ref := range []struct{ repository, tag string }{{"missing/image", "latest"}, {"team/app", "absent"}} {
				_, err := resolver.Resolve(ctx, r.host()+"/"+ref.repository, ref.tag, credentials)
				var response *transport.Error
				if !errors.As(err, &response) || response.StatusCode != 404 {
					t.Fatalf("missing image must produce real 404, got %v", err)
				}
			}
			if private {
				for _, wrong := range []*imageresolver.BasicAuth{nil, {Username: username, Password: "wrong"}, {Username: "wrong", Password: password}} {
					_, err := resolver.Resolve(ctx, r.host()+"/team/app", "latest", wrong)
					var response *transport.Error
					if !errors.As(err, &response) || response.StatusCode != 401 {
						t.Fatalf("wrong credentials must produce real 401, got %v", err)
					}
				}
				check("team/app", "latest", first)
				if info, err := os.Stat(r.root + "/htpasswd"); err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("credential file permissions: %v (%v)", info, err)
				}
			}
			moved, err := r.publish(ctx, "team/app", "latest", "second")
			if err != nil || moved != second {
				t.Fatalf("retag got %q, %v; want %q", moved, err, second)
			}
			check("team/app", "latest", second)
			check("team/other", "v2", second)
			address, root := r.host(), r.root
			if err := r.close(); err != nil {
				t.Fatal(err)
			}
			connection, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				connection.Close()
				t.Fatal("registry listener remained open after cleanup")
			}
			if root != "" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("credential directory remains: %v", err)
				}
			}
		})
	}
}
