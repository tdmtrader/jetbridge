package diskserver_test

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar/objectstore"
)

func TestTLSReadFailuresPreserveInfrastructureAndContextErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hangar-Store-ID", "test-store")
		if r.URL.Path == "/v1/identity" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		if r.URL.Query().Get("key") == "cancel" {
			<-r.Context().Done()
		}
		// Returning before Content-Length bytes have been written truncates the
		// TLS response without emitting the successful stream trailer.
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	f := fixture{server: server, ca: ca, tokens: map[string]string{"publisher": strings.Repeat("p", 32)}}
	client := f.client(t, "publisher")
	for _, key := range []string{"truncated", "cancel"} {
		t.Run(key, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body, err := client.OpenExact(ctx, "outputs", key, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if key == "cancel" {
				cancel()
			}
			_, err = io.ReadAll(body)
			wantCause := io.ErrUnexpectedEOF
			if key == "cancel" {
				wantCause = context.Canceled
			}
			if !errors.Is(err, wantCause) {
				t.Fatalf("read error %v must retain %v", err, wantCause)
			}
			if key == "truncated" && !errors.Is(err, objectstore.ErrInfrastructure) {
				t.Fatalf("truncated response must be an infrastructure error: %v", err)
			}
		})
	}
}
