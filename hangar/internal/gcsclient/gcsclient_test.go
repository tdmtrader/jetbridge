package gcsclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEndpointAcceptsSharedDurableRoot(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"http://127.0.0.1:4443", "http://127.0.0.1:4443/storage/v1/"},
		{"http://127.0.0.1:4443/", "http://127.0.0.1:4443/storage/v1/"},
		{"http://127.0.0.1:4443/storage/v1", "http://127.0.0.1:4443/storage/v1/"},
		{"http://127.0.0.1:4443/storage/v1/", "http://127.0.0.1:4443/storage/v1/"},
	} {
		got, err := NormalizeEndpoint(tc.input)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestNormalizeEndpointRefusesWhatItCannotPlaceOnTheJSONBase(t *testing.T) {
	for _, input := range []string{
		"", "not-a-url", "http://127.0.0.1:4443/storage/v2",
		"http://127.0.0.1:4443?alt=json", "http://127.0.0.1:4443#fragment",
	} {
		_, err := NormalizeEndpoint(input)
		require.Error(t, err, "endpoint %q", input)
	}
}

func TestNewRootEndpointUsesJSONAPIBase(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"bucket"}`))
	}))
	t.Cleanup(server.Close)
	client, err := New(context.Background(), server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	_, err = client.Bucket("bucket").Attrs(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/storage/v1/b/bucket", path)
}
