package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
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
	env := BuildEnvelope(msg)

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

func TestBuildEnvelope_OmitsEmptyFields(t *testing.T) {
	msg := homerun.Message{
		Title:    "ping",
		Severity: "info",
	}
	env := BuildEnvelope(msg)
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
	env := BuildEnvelope(msg)
	card := env.Attachments[0].Content

	if len(card.Body) != 1 {
		t.Errorf("expected only header container in body, got %d elements: %+v", len(card.Body), card.Body)
	}
}

func TestBuildEnvelope_DefaultsWhenTitleAndMessageEmpty(t *testing.T) {
	env := BuildEnvelope(homerun.Message{Severity: "critical"})
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
	env := BuildEnvelope(homerun.Message{
		Title:    "boom",
		Severity: "critical",
		URL:      "https://x",
	})
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
	err := n.Send(context.Background(), homerun.Message{
		Title:    "fire",
		Severity: "critical",
	})
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
	err := n.Send(context.Background(), homerun.Message{Title: "x"})
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("error should mention status and body, got: %v", err)
	}
}

func TestTeamsNotifier_SendRejectsEmptyURL(t *testing.T) {
	n := NewTeamsNotifier("", nil)
	err := n.Send(context.Background(), homerun.Message{Title: "x"})
	if err == nil {
		t.Fatal("expected error when webhook URL is empty")
	}
}

func TestTeamsNotifier_ImplementsNotifier(t *testing.T) {
	var _ Notifier = (*TeamsNotifier)(nil)
}
