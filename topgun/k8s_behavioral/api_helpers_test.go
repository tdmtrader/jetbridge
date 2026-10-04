package behavioral_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vito/go-sse/sse"
	"golang.org/x/oauth2"
)

// ---------------------------------------------------------------------
// Concourse API helpers
// ---------------------------------------------------------------------

// insecureHTTPClient returns an HTTP client that skips TLS verification
// when the ATC URL uses HTTPS (self-signed certs).
func insecureHTTPClient() *http.Client {
	if strings.HasPrefix(config.ATCURL, "https://") {
		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}
	return &http.Client{}
}

// fetchToken retrieves an OAuth2 token from the Concourse instance,
// using an insecure HTTP client for self-signed certs.
func fetchToken() *oauth2.Token {
	GinkgoHelper()

	ctx := context.Background()
	if strings.HasPrefix(config.ATCURL, "https://") {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: transport})
	}

	oauth2Config := oauth2.Config{
		ClientID:     "fly",
		ClientSecret: "Zmx5",
		Endpoint:     oauth2.Endpoint{TokenURL: config.ATCURL + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "email", "federated:id"},
	}

	token, err := oauth2Config.PasswordCredentialsToken(ctx, config.ATCUsername, config.ATCPassword)
	Expect(err).ToNot(HaveOccurred())
	return token
}

// apiGet performs an authenticated HTTP GET against the ATC and returns
// the status code and raw response body.
func apiGet(path string) (int, []byte) {
	GinkgoHelper()

	token := fetchToken()

	url := config.ATCURL + path
	req, err := http.NewRequest("GET", url, nil)
	Expect(err).ToNot(HaveOccurred())

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	client := insecureHTTPClient()
	resp, err := client.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())

	return resp.StatusCode, body
}

// apiGetJSON performs an authenticated HTTP GET and unmarshals the JSON
// response into the provided target.
func apiGetJSON(path string, target interface{}) {
	GinkgoHelper()

	status, body := apiGet(path)
	Expect(status).To(Equal(http.StatusOK),
		fmt.Sprintf("GET %s returned status %d: %s", path, status, string(body)),
	)

	err := json.Unmarshal(body, target)
	Expect(err).ToNot(HaveOccurred(),
		fmt.Sprintf("failed to unmarshal response from GET %s: %s", path, string(body)),
	)
}

// getBuildEvents streams the events for a given build ID and returns
// the event types as a list of strings. The server sends an explicit end
// event, then holds the connection until the client closes it. Keep the
// timeout for incomplete streams, but finish when the complete replay ends.
func getBuildEvents(buildID string) []string {
	GinkgoHelper()

	token := fetchToken()

	url := fmt.Sprintf("%s/api/v1/builds/%s/events", config.ATCURL, buildID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	Expect(err).ToNot(HaveOccurred())

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	client := insecureHTTPClient()
	resp, err := client.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer resp.Body.Close()

	Expect(resp.StatusCode).To(Equal(http.StatusOK),
		fmt.Sprintf("GET %s returned status %d", url, resp.StatusCode),
	)

	// Preserve the previous bounded read (1 MiB plus at most one 32 KiB
	// chunk). Use the same SSE framing as the client, including frames split
	// across HTTP reads; every event preceding the terminal frame is retained.
	stream := sse.NewReadCloser(io.NopCloser(io.LimitReader(resp.Body, 1024*1024+32*1024)))
	var eventTypes []string
	for {
		frame, err := stream.Next()
		if err != nil || frame.Name == "end" {
			break
		}
		var envelope struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(frame.Data, &envelope) == nil && envelope.Event != "" {
			eventTypes = append(eventTypes, envelope.Event)
		}
	}

	return eventTypes
}

// getBuildPreparation returns the build preparation object for the
// given build ID as a generic map.
func getBuildPreparation(buildID string) map[string]interface{} {
	GinkgoHelper()

	path := fmt.Sprintf("/api/v1/builds/%s/preparation", buildID)
	var result map[string]interface{}
	apiGetJSON(path, &result)

	return result
}

// getResourceVersions returns the versions of a resource in a pipeline
// via the Concourse API.
func getResourceVersions(pipeline, resource string) []map[string]interface{} {
	GinkgoHelper()

	path := fmt.Sprintf("/api/v1/teams/main/pipelines/%s/resources/%s/versions", pipeline, resource)
	var result []map[string]interface{}
	apiGetJSON(path, &result)

	return result
}

// splitLines splits a string into lines, handling both \n and \r\n.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
