package commands

import (
	"fmt"
	"io"
	"net/url"

	"github.com/concourse/concourse/agent/pagination"
)

func addAgentHistoryCursor(query url.Values, raw string) error {
	if raw == "" {
		return nil
	}
	if _, err := pagination.Decode(raw); err != nil {
		return fmt.Errorf("invalid agent history cursor")
	}
	query.Set("cursor", raw)
	return nil
}

func reportNextAgentHistoryCursor(writer io.Writer, kind, raw string) error {
	if raw == "" {
		return nil
	}
	if _, err := pagination.Decode(raw); err != nil {
		return fmt.Errorf("server returned invalid %s cursor %q", kind, raw)
	}
	_, err := fmt.Fprintf(writer, "# next cursor: %s\n", raw)
	return err
}
