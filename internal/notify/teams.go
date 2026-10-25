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
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

const (
	adaptiveCardSchema      = "http://adaptivecards.io/schemas/adaptive-card.json"
	adaptiveCardVersion     = "1.4"
	adaptiveCardContentType = "application/vnd.microsoft.card.adaptive"

	defaultTitle = "Notification"

	// resolvedTitlePrefix opens the title of a resolved alert's card (#71).
	resolvedTitlePrefix = "RESOLVED: "

	// ObjectIDFooterPrefix starts the card's last TextBlock, which carries the
	// message's objectId; the sink reads the id back from it.
	ObjectIDFooterPrefix = "objectId "

	defaultHTTPTimeout = 10 * time.Second
)

// TeamsNotifier posts homerun.Message records to a Microsoft Teams channel via
// a Power Automate "post-message-in-chat" webhook (the supported replacement
// for the deprecated O365 connector).
type TeamsNotifier struct {
	webhookURL string
	client     *http.Client
	card       config.CardConfig
}

// NewTeamsNotifier builds a notifier that posts to webhookURL. Pass nil for
// client to use a default with a 10s timeout.
func NewTeamsNotifier(webhookURL string, client *http.Client) *TeamsNotifier {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &TeamsNotifier{webhookURL: webhookURL, client: client}
}

// WithCard sets the output's card config (custom facts) and returns n.
func (n *TeamsNotifier) WithCard(card config.CardConfig) *TeamsNotifier {
	n.card = card
	return n
}

// Send renders msg into an Adaptive Card and POSTs it to the configured webhook.
// Non-2xx responses are returned as errors with the response body included.
func (n *TeamsNotifier) Send(ctx context.Context, msg models.CaughtMessage) error {
	if n.webhookURL == "" {
		return permanent(fmt.Errorf("teams: webhook URL is empty"))
	}

	envelope := BuildEnvelopeWithCard(msg, n.card)
	body, err := json.Marshal(envelope)
	if err != nil {
		return permanent(fmt.Errorf("teams: marshal envelope: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return permanent(fmt.Errorf("teams: build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("teams: POST webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newStatusError(resp, fmt.Sprintf("teams: webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))))
	}

	return nil
}

// BuildEnvelope maps a caught message to the Power Automate Adaptive-Card
// envelope. Pure function — exported for testing.
func BuildEnvelope(msg models.CaughtMessage) models.TeamsEnvelope {
	return BuildEnvelopeWithCard(msg, config.CardConfig{})
}

// BuildEnvelopeWithCard is BuildEnvelope with an output's card config: its
// facts, when set, replace the default FactSet.
func BuildEnvelopeWithCard(msg models.CaughtMessage, card config.CardConfig) models.TeamsEnvelope {
	content := buildCard(msg.Message, msg.ObjectID, card)
	return models.TeamsEnvelope{
		Type:    "message",
		Summary: content.FallbackText,
		Attachments: []models.TeamsAttachment{{
			ContentType: adaptiveCardContentType,
			Content:     content,
		}},
	}
}

func buildCard(msg homerun.Message, objectID string, cfg config.CardConfig) models.AdaptiveCard {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = defaultTitle
	}
	body := strings.TrimSpace(msg.Message)

	// A resolved alert is green whatever its severity, and says so in the
	// title; the message's own "Resolved: " would only repeat it.
	style, color, label := severityToStyle(msg.Severity), severityToTextColor(msg.Severity), msg.Severity
	resolved := AlertStatus(msg) == StatusResolved
	if resolved {
		style, color, label = "good", "good", StatusResolved
		if !hasPrefixFold(title, resolvedTitlePrefix) {
			title = resolvedTitlePrefix + title
		}
		if hasPrefixFold(body, resolvedPrefix) {
			body = strings.TrimSpace(body[len(resolvedPrefix):])
		}
	}
	if body == "" {
		body = title
	}

	header := models.CardElement{
		Type:  "Container",
		Style: style,
		Items: []models.CardElement{
			{
				Type:   "TextBlock",
				Text:   title,
				Size:   "Large",
				Weight: "Bolder",
				Wrap:   true,
				Color:  color,
			},
			{
				Type: "TextBlock",
				Text: body,
				Wrap: true,
			},
		},
	}

	card := models.AdaptiveCard{
		Schema:       adaptiveCardSchema,
		Type:         "AdaptiveCard",
		Version:      adaptiveCardVersion,
		FallbackText: buildFallbackText(label, title, body),
		Body:         []models.CardElement{header},
	}

	facts := buildFacts(msg, resolved)
	if len(cfg.Facts) > 0 {
		facts = buildConfiguredFacts(msg, cfg.Facts)
	}
	if len(facts) > 0 {
		card.Body = append(card.Body, models.CardElement{
			Type:  "FactSet",
			Facts: facts,
		})
	}

	// Small and dimmed, last: the id to look the message up by, in the sink
	// (assert-card --object-id) or the logs, without competing with the facts.
	if id := strings.TrimSpace(objectID); id != "" {
		card.Body = append(card.Body, models.CardElement{
			Type:     "TextBlock",
			Text:     ObjectIDFooterPrefix + id,
			Size:     "Small",
			IsSubtle: true,
			Wrap:     true,
		})
	}

	if url := strings.TrimSpace(msg.URL); url != "" {
		card.Actions = []models.CardAction{{
			Type:  "Action.OpenUrl",
			Title: "Open",
			URL:   url,
		}}
	}

	return card
}

// buildFallbackText renders the card as one plain-text line for hosts that
// cannot render Adaptive Cards: "[severity] title: body". The severity prefix
// is dropped when empty, and the body when it only repeats the title (which
// buildCard does when the message has no body of its own).
func buildFallbackText(severity, title, body string) string {
	text := title
	if body != title {
		text += ": " + body
	}
	if s := strings.TrimSpace(severity); s != "" {
		text = "[" + s + "] " + text
	}
	return text
}

// buildFacts builds the FactSet rows, skipping any field whose value is empty
// after trimming. Order is fixed so card rendering is deterministic. A
// resolved alert's time is when it ended (omni-pitcher stamps it with
// endsAt), so the row says Resolved.
func buildFacts(msg homerun.Message, resolved bool) []models.CardFact {
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
	if resolved {
		add("Resolved", msg.Timestamp)
	} else {
		add("Time", msg.Timestamp)
	}
	return facts
}

// buildConfiguredFacts builds the FactSet rows an output's card config asks
// for, in its order, skipping any row whose value is empty.
func buildConfiguredFacts(msg homerun.Message, rows []config.CardFactConfig) []models.CardFact {
	facts := make([]models.CardFact, 0, len(rows))
	for _, r := range rows {
		value := getField(msg, r.Field)
		if strings.TrimSpace(r.Tag) != "" {
			value = tagValue(msg.Tags, r.Tag)
		}
		if v := strings.TrimSpace(value); v != "" {
			facts = append(facts, models.CardFact{Title: r.Title, Value: v})
		}
	}
	return facts
}

// tagValue returns the value of the first `key=value` entry in the
// comma-separated tags whose key equals key (case-insensitive), or "".
func tagValue(tags, key string) string {
	key = strings.TrimSpace(key)
	for _, t := range strings.Split(tags, ",") {
		k, v, ok := strings.Cut(t, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
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
