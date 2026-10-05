package atccmd

import (
	"net/url"
	"testing"

	"github.com/concourse/flag/v2"
)

func TestSecureCookiesFollowAnHTTPSExternalURL(t *testing.T) {
	for _, tc := range []struct {
		url    string
		forced bool
		want   bool
	}{
		{"https://ci.example", false, true},
		{"http://ci.example", false, false},
		{"http://ci.example", true, true},
		{"", false, false},
	} {
		cmd := &RunCommand{}
		if tc.url != "" {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			cmd.ExternalURL = flag.URL{URL: u}
		}
		cmd.Auth.AuthFlags.SecureCookies = tc.forced
		if got := cmd.secureCookies(); got != tc.want {
			t.Errorf("url=%q forced=%v: secureCookies()=%v, want %v", tc.url, tc.forced, got, tc.want)
		}
	}
}
