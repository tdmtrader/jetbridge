// Package notify implements the §8.4 notification channel: a single generic
// webhook the ATC POSTs on HITL questions/checkpoints (this workstream) and,
// later, ticket state changes and budget stops (their owners call Notifier).
// The ticket page remains the source of truth; the webhook is fan-out only.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notification is the exact §8.4 webhook payload.
type Notification struct {
	Kind     string `json:"kind"` // question | checkpoint | state | budget
	TicketID int    `json:"ticket_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Body     string `json:"body"`
}

//counterfeiter:generate . Notifier
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

type webhookNotifier struct {
	url    string
	client *http.Client
}

// NewWebhookNotifier posts notifications to webhookURL. A nil client gets a
// 10s-timeout default (never hang a poller on a dead webhook).
func NewWebhookNotifier(webhookURL string, client *http.Client) Notifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &webhookNotifier{url: webhookURL, client: client}
}

func (w *webhookNotifier) Notify(ctx context.Context, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
