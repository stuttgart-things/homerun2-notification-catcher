package notify

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// The rich layout (#82): card.layout: rich.
const (
	richCardVersion = "1.5"

	// richDetailsID is the hidden Container the Details button toggles.
	richDetailsID = "details"
)

// buildRichCard renders msg as the rich layout: a severity-styled header
// with emoji, short headline, subtitle and optional avatar; the full title
// and message, or the first card.text template that resolves; a FactSet with the time in the reader's time zone; buttons;
// tags and objectId collapsed behind "Details". Every part that has no data
// is left out, so a message without the tags a template asks for still
// renders, with its title as the headline.
func buildRichCard(msg homerun.Message, objectID string, cfg config.CardConfig) models.AdaptiveCard {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = defaultTitle
	}
	body := strings.TrimSpace(msg.Message)
	style, emoji := richSeverity(msg.Severity)
	label := msg.Severity
	resolved := AlertStatus(msg) == StatusResolved
	if resolved {
		style, emoji, label = "good", "✅", StatusResolved
		if !hasPrefixFold(title, resolvedTitlePrefix) {
			title = resolvedTitlePrefix + title
		}
		if hasPrefixFold(body, resolvedPrefix) {
			body = strings.TrimSpace(body[len(resolvedPrefix):])
		}
	}

	lookup := templateLookup(msg)
	headline, ok := renderFirst(cfg.Headline, lookup)
	switch {
	case !ok:
		headline = title
	case resolved && !hasPrefixFold(headline, resolvedTitlePrefix):
		headline = resolvedTitlePrefix + headline
	}

	texts := []models.CardElement{{
		Type:   "TextBlock",
		Text:   strings.TrimSpace(emoji + " " + headline),
		Weight: "Bolder",
		Size:   "Large",
		Wrap:   true,
	}}
	if subtitle, ok := renderFirst(cfg.Subtitle, lookup); ok {
		texts = append(texts, models.CardElement{
			Type:     "TextBlock",
			Text:     subtitle,
			IsSubtle: true,
			Spacing:  "None",
			Wrap:     true,
		})
	}
	headerItems := texts
	if avatar := avatarURL(cfg.Avatar, msg); avatar != "" {
		headerItems = []models.CardElement{{
			Type: "ColumnSet",
			Columns: []models.CardElement{
				{Type: "Column", Width: "auto", Items: []models.CardElement{{
					Type: "Image", URL: avatar, Size: "Small", Style: "Person", AltText: strings.TrimSpace(msg.Author),
				}}},
				{Type: "Column", Width: "stretch", VerticalContentAlignment: "Center", Items: texts},
			},
		}}
	}

	fallbackBody := body
	if fallbackBody == "" {
		fallbackBody = title
	}
	card := models.AdaptiveCard{
		Schema:       adaptiveCardSchema,
		Type:         "AdaptiveCard",
		Version:      richCardVersion,
		FallbackText: buildFallbackText(label, title, fallbackBody),
		MSTeams:      &models.CardMSTeams{Width: "Full"},
		Body: []models.CardElement{{
			Type:  "Container",
			Style: style,
			Bleed: true,
			Items: headerItems,
		}},
	}
	if text, ok := renderFirst(cfg.Text, lookup); ok {
		card.Body = append(card.Body, models.CardElement{Type: "TextBlock", Text: text, Wrap: true})
	} else {
		if headline != title {
			card.Body = append(card.Body, models.CardElement{Type: "TextBlock", Text: title, Wrap: true})
		}
		if body != "" && body != title {
			card.Body = append(card.Body, models.CardElement{Type: "TextBlock", Text: body, Wrap: true})
		}
	}

	facts := richFacts(msg, resolved)
	if len(cfg.Facts) > 0 {
		facts = buildConfiguredFacts(msg, cfg.Facts, localTime)
	}
	if len(facts) > 0 {
		card.Body = append(card.Body, models.CardElement{Type: "FactSet", Facts: facts})
	}

	var details []models.CardElement
	if tags := displayTags(msg.Tags); tags != "" {
		details = append(details, models.CardElement{
			Type: "TextBlock", Text: "Tags: " + tags, IsSubtle: true, Size: "Small", Wrap: true,
		})
	}
	// The sink reads the objectId back from this block (ObjectIDFooterPrefix).
	if id := strings.TrimSpace(objectID); id != "" {
		details = append(details, models.CardElement{
			Type: "TextBlock", Text: ObjectIDFooterPrefix + id, IsSubtle: true, Size: "Small", FontType: "Monospace", Wrap: true,
		})
	}
	if len(details) > 0 {
		hidden := false
		card.Body = append(card.Body, models.CardElement{
			Type: "Container", ID: richDetailsID, IsVisible: &hidden, Items: details,
		})
	}

	card.Actions = richActions(msg, cfg.Actions, lookup)
	if len(details) > 0 {
		card.Actions = append(card.Actions, models.CardAction{
			Type: "Action.ToggleVisibility", Title: "Details", TargetElements: []string{richDetailsID},
		})
	}
	return card
}

// richSeverity maps a severity to the header's Container style and emoji. An
// unknown or empty severity counts as info, as in the filters.
func richSeverity(severity string) (style, emoji string) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "error":
		return "attention", "🔥"
	case "warning":
		return "warning", "⚠️"
	case "success":
		return "good", "✅"
	case "debug":
		return "default", "🐞"
	default:
		return "accent", "ℹ️"
	}
}

// templateLookup resolves {tag:x} and {field:x} against msg.
func templateLookup(msg homerun.Message) func(kind, key string) string {
	return func(kind, key string) string {
		if kind == "tag" {
			return tagValue(msg.Tags, key)
		}
		return getField(msg, key)
	}
}

// renderFirst renders the first template of ts whose placeholders all
// resolve.
func renderFirst(ts config.Templates, lookup func(kind, key string) string) (string, bool) {
	for _, tpl := range ts {
		if out, ok := config.RenderTemplate(tpl, lookup); ok {
			return out, true
		}
	}
	return "", false
}

// richFacts is the rich layout's default FactSet: severity and tags are in
// the header and the details already.
func richFacts(msg homerun.Message, resolved bool) []models.CardFact {
	facts := make([]models.CardFact, 0, 3)
	add := func(title, value string) {
		if v := strings.TrimSpace(value); v != "" {
			facts = append(facts, models.CardFact{Title: title, Value: v})
		}
	}
	add("Author", msg.Author)
	add("System", msg.System)
	if resolved {
		add("Resolved", localTime(msg.Timestamp))
	} else {
		add("Time", localTime(msg.Timestamp))
	}
	return facts
}

// localTime renders an RFC 3339 timestamp with the Teams date/time functions,
// which show it in the reader's time zone. Anything else is shown as is.
func localTime(ts string) string {
	ts = strings.TrimSpace(ts)
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	iso := t.UTC().Format("2006-01-02T15:04:05Z")
	return fmt.Sprintf("{{DATE(%s, SHORT)}}, {{TIME(%s)}}", iso, iso)
}

// displayTags lists comma-separated tags with a space after each comma.
func displayTags(tags string) string {
	var out []string
	for _, t := range strings.Split(tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", ")
}

// richActions builds the OpenUrl buttons: the configured ones, else "Open"
// for the message url. A button whose URL is empty or not an absolute
// http(s) URL is left out -- Teams rejects the whole card for a bad one.
func richActions(msg homerun.Message, cfg []config.CardActionConfig, lookup func(kind, key string) string) []models.CardAction {
	if len(cfg) == 0 {
		cfg = []config.CardActionConfig{{Title: "Open", Field: "url"}}
	}
	var out []models.CardAction
	for _, a := range cfg {
		target := strings.TrimSpace(getField(msg, a.Field))
		if strings.TrimSpace(a.Template) != "" {
			target, _ = config.RenderTemplate(a.Template, lookup)
		}
		if !isWebURL(target) {
			continue
		}
		out = append(out, models.CardAction{Type: "Action.OpenUrl", Title: strings.TrimSpace(a.Title), URL: target})
	}
	return out
}

func isWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// githubLogin matches a GitHub user name: alphanumerics and single inner
// hyphens, at most 39 characters.
var githubLogin = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// nonPersonAuthors are authors that are tools, not people: no avatar.
var nonPersonAuthors = map[string]struct{}{
	"alertmanager": {}, "grafana": {}, "prometheus": {}, "flux": {}, "fluxcd": {},
	"argocd": {}, "argo": {}, "kubernetes": {}, "renovate": {}, "dependabot": {},
	"github": {}, "github-actions": {}, "actions": {}, "ci": {}, "jenkins": {},
	"tekton": {}, "system": {}, "unknown": {}, "bot": {},
}

// avatarURL is the header image of the avatar setting, or "": for github,
// https://github.com/<author>.png when the author looks like a person's
// GitHub login (not a bot, a tool or the message's system).
func avatarURL(avatar string, msg homerun.Message) string {
	if !strings.EqualFold(strings.TrimSpace(avatar), config.CardAvatarGitHub) {
		return ""
	}
	author := strings.TrimSpace(msg.Author)
	lower := strings.ToLower(author)
	if len(author) > 39 || !githubLogin.MatchString(author) || strings.HasSuffix(lower, "-bot") ||
		strings.HasPrefix(lower, "homerun") || strings.EqualFold(author, strings.TrimSpace(msg.System)) {
		return ""
	}
	if _, tool := nonPersonAuthors[lower]; tool {
		return ""
	}
	return "https://github.com/" + author + ".png"
}
