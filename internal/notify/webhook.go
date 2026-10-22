package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// WebhookPayload is the JSON a webhook output sends: the homerun.Message's
// fields plus its objectId (#64). Additive, so a receiver that decodes a
// plain homerun.Message keeps working.
type WebhookPayload struct {
	homerun.Message
	ObjectID string `json:"objectId,omitempty"`
}

// WebhookNotifier POSTs the homerun.Message JSON (WebhookPayload) to an arbitrary HTTP
// endpoint. Use it for PagerDuty/Opsgenie-style ingest URLs, custom internal
// services, or anywhere the receiver wants the Message struct directly rather
// than a vendor-specific payload.
type WebhookNotifier struct {
	url     string
	method  string
	headers map[string]string
	client  *http.Client
}

// NewWebhookNotifier builds a notifier targeting url. Pass an empty method to
// default to POST. Pass nil for client to use a default with the same 10s
// timeout the Teams notifier applies.
func NewWebhookNotifier(url, method string, headers map[string]string, client *http.Client) *WebhookNotifier {
	if strings.TrimSpace(method) == "" {
		method = http.MethodPost
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WebhookNotifier{
		url:     url,
		method:  strings.ToUpper(method),
		headers: headers,
		client:  client,
	}
}

// Send marshals msg to JSON and dispatches it. Non-2xx responses are surfaced
// with status code and (truncated) body so the operator can diagnose.
func (n *WebhookNotifier) Send(ctx context.Context, msg models.CaughtMessage) error {
	if strings.TrimSpace(n.url) == "" {
		return permanent(fmt.Errorf("webhook: URL is empty"))
	}

	body, err := json.Marshal(WebhookPayload{Message: msg.Message, ObjectID: msg.ObjectID})
	if err != nil {
		return permanent(fmt.Errorf("webhook: marshal message: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, n.method, n.url, bytes.NewReader(body))
	if err != nil {
		return permanent(fmt.Errorf("webhook: build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %s %s: %w", n.method, n.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newStatusError(resp, fmt.Sprintf("webhook: %s returned %d: %s", n.url, resp.StatusCode, strings.TrimSpace(string(respBody))))
	}
	return nil
}
