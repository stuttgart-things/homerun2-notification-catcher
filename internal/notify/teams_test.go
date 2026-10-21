package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

func TestSeverityToStyle(t *testing.T) {
	cases := []struct {
		severity string
		want     string
	}{
		{"critical", "attention"},
		{"error", "attention"},
		{"warning", "warning"},
		{"success", "good"},
		{"info", "default"},
		{"debug", "default"},
		{"", "default"},
		{"  WARNING  ", "warning"},
		{"unknown-level", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.severity, func(t *testing.T) {
			if got := severityToStyle(tc.severity); got != tc.want {
				t.Errorf("severityToStyle(%q) = %q, want %q", tc.severity, got, tc.want)
			}
		})
	}
}

func TestBuildEnvelope_Shape(t *testing.T) {
	msg := homerun.Message{
		Title:     "Disk almost full",
		Message:   "node01: 92% used",
		Severity:  "warning",
		Author:    "alertmanager",
		System:    "kubernetes",
		Tags:      "infra,storage",
		Timestamp: "2026-05-24T10:00:00Z",
		URL:       "https://grafana.example/d/abc",
	}
	env := BuildEnvelope(caught(msg))

	if env.Type != "message" {
		t.Errorf("envelope.Type = %q, want %q", env.Type, "message")
	}
	if len(env.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(env.Attachments))
	}
	att := env.Attachments[0]
	if att.ContentType != adaptiveCardContentType {
		t.Errorf("attachment.ContentType = %q, want %q", att.ContentType, adaptiveCardContentType)
	}

	card := att.Content
	if card.Type != "AdaptiveCard" {
		t.Errorf("card.Type = %q, want AdaptiveCard", card.Type)
	}
	if card.Schema != adaptiveCardSchema {
		t.Errorf("card.Schema = %q, want %q", card.Schema, adaptiveCardSchema)
	}
	if card.Version != adaptiveCardVersion {
		t.Errorf("card.Version = %q, want %q", card.Version, adaptiveCardVersion)
	}
	if len(card.Body) != 2 {
		t.Fatalf("card body = %d elements, want 2 (header container + factset)", len(card.Body))
	}

	header := card.Body[0]
	if header.Type != "Container" || header.Style != "warning" {
		t.Errorf("header = %+v, want Container/warning style", header)
	}
	if len(header.Items) != 2 || header.Items[0].Text != "Disk almost full" || header.Items[1].Text != "node01: 92% used" {
		t.Errorf("header items unexpected: %+v", header.Items)
	}

	factset := card.Body[1]
	if factset.Type != "FactSet" {
		t.Fatalf("expected FactSet, got %s", factset.Type)
	}
	wantFacts := []models.CardFact{
		{Title: "Severity", Value: "warning"},
		{Title: "System", Value: "kubernetes"},
		{Title: "Author", Value: "alertmanager"},
		{Title: "Tags", Value: "infra,storage"},
		{Title: "Time", Value: "2026-05-24T10:00:00Z"},
	}
	if len(factset.Facts) != len(wantFacts) {
		t.Fatalf("facts = %d, want %d (%+v)", len(factset.Facts), len(wantFacts), factset.Facts)
	}
	for i, want := range wantFacts {
		if factset.Facts[i] != want {
			t.Errorf("fact[%d] = %+v, want %+v", i, factset.Facts[i], want)
		}
	}

	if len(card.Actions) != 1 || card.Actions[0].URL != "https://grafana.example/d/abc" {
		t.Errorf("actions unexpected: %+v", card.Actions)
	}
}

// A Teams client or notification preview that cannot render the card shows
// only "Card - access it on https://go.skype.com/cards.unsupported" unless the
// card carries fallbackText (stuttgart-things/stuttgart-things#3490).
func TestBuildEnvelope_FallbackText(t *testing.T) {
	cases := []struct {
		name string
		msg  homerun.Message
		want string
	}{
		{
			name: "severity, title and body",
			msg:  homerun.Message{Title: "Disk almost full", Message: "node01: 92% used", Severity: "warning"},
			want: "[warning] Disk almost full: node01: 92% used",
		},
		{
			name: "no severity",
			msg:  homerun.Message{Title: "hello", Message: "world"},
			want: "hello: world",
		},
		{
			name: "body falls back to the title and is not repeated",
			msg:  homerun.Message{Title: "ping", Severity: "info"},
			want: "[info] ping",
		},
		{
			name: "nothing set",
			msg:  homerun.Message{},
			want: defaultTitle,
		},
		{
			name: "whitespace is trimmed",
			msg:  homerun.Message{Title: "  Resolved: X  ", Message: "  gone  ", Severity: "  success "},
			want: "[success] Resolved: X: gone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := BuildEnvelope(caught(tc.msg)).Attachments[0].Content
			if card.FallbackText != tc.want {
				t.Errorf("fallbackText = %q, want %q", card.FallbackText, tc.want)
			}
		})
	}
}

func TestBuildEnvelope_OmitsEmptyFields(t *testing.T) {
	msg := homerun.Message{
		Title:    "ping",
		Severity: "info",
	}
	env := BuildEnvelope(caught(msg))
	card := env.Attachments[0].Content

	// FactSet should contain only "Severity" (other fields empty).
	if len(card.Body) != 2 {
		t.Fatalf("expected 2 body elements, got %d", len(card.Body))
	}
	factset := card.Body[1]
	if len(factset.Facts) != 1 || factset.Facts[0].Title != "Severity" {
		t.Errorf("facts should contain only Severity, got %+v", factset.Facts)
	}

	// No URL → no action bar.
	if len(card.Actions) != 0 {
		t.Errorf("actions should be empty when URL is unset, got %+v", card.Actions)
	}
}

func TestBuildEnvelope_NoFactsetWhenAllOptionalEmpty(t *testing.T) {
	msg := homerun.Message{
		Title:   "hello",
		Message: "world",
		// no Severity, System, Author, Tags, Timestamp
	}
	env := BuildEnvelope(caught(msg))
	card := env.Attachments[0].Content

	if len(card.Body) != 1 {
		t.Errorf("expected only header container in body, got %d elements: %+v", len(card.Body), card.Body)
	}
}

func TestBuildEnvelope_DefaultsWhenTitleAndMessageEmpty(t *testing.T) {
	env := BuildEnvelope(caught(homerun.Message{Severity: "critical"}))
	card := env.Attachments[0].Content
	header := card.Body[0]
	if header.Items[0].Text != defaultTitle {
		t.Errorf("title should fall back to %q, got %q", defaultTitle, header.Items[0].Text)
	}
	if header.Items[1].Text != defaultTitle {
		t.Errorf("message should fall back to title (%q), got %q", defaultTitle, header.Items[1].Text)
	}
	if header.Style != "attention" {
		t.Errorf("critical severity should map to attention style, got %q", header.Style)
	}
}

func TestBuildEnvelope_JSONMarshalsCleanly(t *testing.T) {
	env := BuildEnvelope(caught(homerun.Message{
		Title:    "boom",
		Severity: "critical",
		URL:      "https://x",
	}))
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"type":"message"`,
		`"contentType":"application/vnd.microsoft.card.adaptive"`,
		`"$schema":"http://adaptivecards.io/schemas/adaptive-card.json"`,
		`"type":"AdaptiveCard"`,
		`"fallbackText":"[critical] boom"`,
		`"Action.OpenUrl"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled JSON missing %q\nfull: %s", want, s)
		}
	}
}

func TestTeamsNotifier_SendPostsEnvelope(t *testing.T) {
	var (
		gotContentType string
		gotBody        []byte
		gotMethod      string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewTeamsNotifier(srv.URL, &http.Client{Timeout: 2 * time.Second})
	err := n.Send(context.Background(), caught(homerun.Message{
		Title:    "fire",
		Severity: "critical",
	}))
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var env models.TeamsEnvelope
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("body is not a valid TeamsEnvelope: %v\nbody: %s", err, gotBody)
	}
	if env.Type != "message" || len(env.Attachments) != 1 {
		t.Errorf("envelope shape unexpected: %+v", env)
	}
}

func TestTeamsNotifier_SendReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	n := NewTeamsNotifier(srv.URL, &http.Client{Timeout: 2 * time.Second})
	err := n.Send(context.Background(), caught(homerun.Message{Title: "x"}))
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("error should mention status and body, got: %v", err)
	}
}

func TestTeamsNotifier_SendRejectsEmptyURL(t *testing.T) {
	n := NewTeamsNotifier("", nil)
	err := n.Send(context.Background(), caught(homerun.Message{Title: "x"}))
	if err == nil {
		t.Fatal("expected error when webhook URL is empty")
	}
}

func TestTeamsNotifier_ImplementsNotifier(t *testing.T) {
	var _ Notifier = (*TeamsNotifier)(nil)
}

func TestBuildEnvelopeWithCard_ConfiguredFacts(t *testing.T) {
	msg := homerun.Message{
		Title:     "WIN 3:1",
		Message:   "Anna 3 : 1 Ben — Anna wins",
		Severity:  "success",
		System:    "tabletennis",
		Tags:      "match=be14fdfc,transition=match_won,sets=3:1,winner=a,player_a=Anna,player_b=Ben,played=11-7 9-11 11-5 11-9",
		Timestamp: "2026-10-18T20:33:55Z",
	}
	card := config.CardConfig{Facts: []config.CardFactConfig{
		{Title: "Spieler A", Tag: "player_a"},
		{Title: "Spieler B", Tag: "PLAYER_B"},
		{Title: "Sätze", Tag: "sets"},
		{Title: "Satzergebnisse", Tag: "played"},
		{Title: "Banner", Tag: "banner"}, // absent: left out
		{Title: "Zeit", Field: "timestamp"},
	}}
	env := BuildEnvelopeWithCard(caught(msg), card)

	body := env.Attachments[0].Content.Body
	if len(body) != 2 || body[1].Type != "FactSet" {
		t.Fatalf("body = %+v, want header + FactSet", body)
	}
	want := []models.CardFact{
		{Title: "Spieler A", Value: "Anna"},
		{Title: "Spieler B", Value: "Ben"},
		{Title: "Sätze", Value: "3:1"},
		{Title: "Satzergebnisse", Value: "11-7 9-11 11-5 11-9"},
		{Title: "Zeit", Value: "2026-10-18T20:33:55Z"},
	}
	got := body[1].Facts
	if len(got) != len(want) {
		t.Fatalf("facts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fact[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildEnvelopeWithCard_EmptyConfigKeepsDefaultFacts(t *testing.T) {
	msg := homerun.Message{Title: "t", Severity: "info", System: "s"}
	a := BuildEnvelope(caught(msg))
	b := BuildEnvelopeWithCard(caught(msg), config.CardConfig{})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("empty card config changed the card:\n%s\n%s", ja, jb)
	}
}

// The objectId is the card's last element, small and subtle (#64); without
// one the card is unchanged.
func TestBuildEnvelope_ObjectIDFooter(t *testing.T) {
	msg := homerun.Message{Title: "t", Message: "b", Severity: "warning", System: "s"}
	card := BuildEnvelope(models.CaughtMessage{Message: msg, ObjectID: "1761000000000-0"}).Attachments[0].Content
	last := card.Body[len(card.Body)-1]
	want := models.CardElement{Type: "TextBlock", Text: "objectId 1761000000000-0", Size: "Small", IsSubtle: true, Wrap: true}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("footer = %+v, want %+v", last, want)
	}
	if card.FallbackText != "[warning] t: b" {
		t.Errorf("fallbackText = %q, the id stays out of the preview line", card.FallbackText)
	}

	plain := BuildEnvelope(caught(msg)).Attachments[0].Content
	if len(plain.Body) != len(card.Body)-1 {
		t.Errorf("no objectId: body has %d elements, want %d", len(plain.Body), len(card.Body)-1)
	}
	for _, e := range plain.Body {
		if strings.HasPrefix(e.Text, ObjectIDFooterPrefix) {
			t.Errorf("footer without an objectId: %+v", e)
		}
	}
}
