package artifactwire

import "fmt"

// ShellPrelude is the opening of every init-container script that reaches the
// daemon on its own node: the port, the daemon address and the wget options.
// The scheme, the port and the TLS options are the same three facts the Go
// client holds, rendered once, so a script cannot dial http at a daemon the
// ATC dials over https.
//
// The host is always ${HOST_IP}, from the Downward API: an init container
// talks to the daemon on the node it landed on and no other.
//
// --no-check-certificate is there because an init container dials its node by
// IP, which is not a certificate SAN, so server authentication cannot succeed
// however correct the deployment is. What the transport buys is
// confidentiality for the one-shot capability a request presents; the
// AUTHORIZATION is that signed, single-use token, verified by the daemon, and
// it is unchanged by this.
func (c *Client) ShellPrelude() string {
	return fmt.Sprintf("PORT=%d\nDAEMON=\"%s://${HOST_IP}:${PORT}\"\nWGET_OPTS=\"%s\"\n", c.port, c.scheme, c.WgetOptions())
}

// WgetOptions is the extra BusyBox wget options a script needs to reach a
// daemon over TLS, or "" when there is none. See ShellPrelude for why.
func (c *Client) WgetOptions() string {
	if c.scheme == "https" {
		return "--no-check-certificate"
	}
	return ""
}
