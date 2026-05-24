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

	homerun "github.com/stuttgart-things/homerun-library/v3"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

const (
	adaptiveCardSchema      = "http://adaptivecards.io/schemas/adaptive-card.json"
	adaptiveCardVersion     = "1.4"
	adaptiveCardContentType = "application/vnd.microsoft.card.adaptive"

	defaultTitle = "Notification"

	defaultHTTPTimeout = 10 * time.Second
)

// TeamsNotifier posts homerun.Message records to a Microsoft Teams channel via
// a Power Automate "post-message-in-chat" webhook (the supported replacement
// for the deprecated O365 connector).
type TeamsNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewTeamsNotifier builds a notifier that posts to webhookURL. Pass nil for
// client to use a default with a 10s timeout.
func NewTeamsNotifier(webhookURL string, client *http.Client) *TeamsNotifier {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &TeamsNotifier{webhookURL: webhookURL, client: client}
}

// Send renders msg into an Adaptive Card and POSTs it to the configured webhook.
// Non-2xx responses are returned as errors with the response body included.
func (n *TeamsNotifier) Send(ctx context.Context, msg homerun.Message) error {
	if n.webhookURL == "" {
		return fmt.Errorf("teams: webhook URL is empty")
	}

	envelope := BuildEnvelope(msg)
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("teams: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("teams: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("teams: POST webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("teams: webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

// BuildEnvelope maps a homerun.Message to the Power Automate Adaptive-Card
// envelope. Pure function — exported for testing.
func BuildEnvelope(msg homerun.Message) models.TeamsEnvelope {
	return models.TeamsEnvelope{
		Type: "message",
		Attachments: []models.TeamsAttachment{{
			ContentType: adaptiveCardContentType,
			Content:     buildCard(msg),
		}},
	}
}

func buildCard(msg homerun.Message) models.AdaptiveCard {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = defaultTitle
	}
	body := strings.TrimSpace(msg.Message)
	if body == "" {
		body = title
	}

	header := models.CardElement{
		Type:  "Container",
		Style: severityToStyle(msg.Severity),
		Items: []models.CardElement{
			{
				Type:   "TextBlock",
				Text:   title,
				Size:   "Large",
				Weight: "Bolder",
				Wrap:   true,
				Color:  severityToTextColor(msg.Severity),
			},
			{
				Type: "TextBlock",
				Text: body,
				Wrap: true,
			},
		},
	}

	card := models.AdaptiveCard{
		Schema:  adaptiveCardSchema,
		Type:    "AdaptiveCard",
		Version: adaptiveCardVersion,
		Body:    []models.CardElement{header},
	}

	if facts := buildFacts(msg); len(facts) > 0 {
		card.Body = append(card.Body, models.CardElement{
			Type:  "FactSet",
			Facts: facts,
		})
	}

	if url := strings.TrimSpace(msg.Url); url != "" {
		card.Actions = []models.CardAction{{
			Type:  "Action.OpenUrl",
			Title: "Open",
			URL:   url,
		}}
	}

	return card
}

// buildFacts builds the FactSet rows, skipping any field whose value is empty
// after trimming. Order is fixed so card rendering is deterministic.
func buildFacts(msg homerun.Message) []models.CardFact {
	facts := make([]models.CardFact, 0, 5)
	add := func(title, value string) {
		if v := strings.TrimSpace(value); v != "" {
			facts = append(facts, models.CardFact{Title: title, Value: v})
		}
	}
	add("Severity", msg.Severity)
	add("System", msg.System)
	add("Author", msg.Author)
	add("Tags", msg.Tags)
	add("Time", msg.Timestamp)
	return facts
}

// severityToStyle maps a homerun.Message severity to an Adaptive Card
// Container style. Adaptive Cards v1.4 supports: default, emphasis, good,
// attention, warning, accent.
func severityToStyle(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "error":
		return "attention"
	case "warning":
		return "warning"
	case "success":
		return "good"
	default:
		return "default"
	}
}

// severityToTextColor returns the TextBlock color attribute matching a
// severity. Adaptive Cards v1.4 supports: default, dark, light, accent, good,
// warning, attention.
func severityToTextColor(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "error":
		return "attention"
	case "warning":
		return "warning"
	case "success":
		return "good"
	default:
		return "default"
	}
}
