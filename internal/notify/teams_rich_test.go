package notify

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

func TestRichSeverity(t *testing.T) {
	cases := map[string][2]string{
		"success": {"good", "✅"}, "warning": {"warning", "⚠️"}, "error": {"attention", "🔥"},
		"CRITICAL": {"attention", "🔥"}, "info": {"accent", "ℹ️"}, "": {"accent", "ℹ️"},
		"weird": {"accent", "ℹ️"}, "debug": {"default", "🐞"},
	}
	for sev, want := range cases {
		if style, emoji := richSeverity(sev); style != want[0] || emoji != want[1] {
			t.Errorf("richSeverity(%q) = %q %q, want %q %q", sev, style, emoji, want[0], want[1])
		}
	}
}

func TestAvatarURL(t *testing.T) {
	cases := []struct {
		avatar, author, system, want string
	}{
		{"github", "patrick-hermann-sva", "homerun2-git-pitcher", "https://github.com/patrick-hermann-sva.png"},
		{"GitHub", "octocat", "", "https://github.com/octocat.png"},
		{"", "octocat", "", ""},
		{"none", "octocat", "", ""},
		{"github", "dependabot[bot]", "", ""},
		{"github", "renovate-bot", "", ""},
		{"github", "grafana", "", ""},
		{"github", "Alertmanager", "", ""},
		{"github", "kubernetes", "kubernetes", ""},
		{"github", "flux-reconciler", "flux-reconciler", ""},
		{"github", "homerun2-omni-pitcher", "", ""},
		{"github", "Patrick Hermann", "", ""},
		{"github", "a@b.c", "", ""},
		{"github", "-x", "", ""},
		{"github", "x--y", "", ""},
		{"github", strings.Repeat("a", 40), "", ""},
		{"github", "", "", ""},
	}
	for _, tc := range cases {
		got := avatarURL(tc.avatar, homerun.Message{Author: tc.author, System: tc.system})
		if got != tc.want {
			t.Errorf("avatarURL(%q, %q) = %q, want %q", tc.avatar, tc.author, got, tc.want)
		}
	}
}

func TestLocalTime(t *testing.T) {
	cases := map[string]string{
		"2026-11-07T21:47:06Z":      "{{DATE(2026-11-07T21:47:06Z, SHORT)}}, {{TIME(2026-11-07T21:47:06Z)}}",
		"2026-11-07T08:15:00+01:00": "{{DATE(2026-11-07T07:15:00Z, SHORT)}}, {{TIME(2026-11-07T07:15:00Z)}}",
		" 2026-11-07T21:47:06.5Z ":  "{{DATE(2026-11-07T21:47:06Z, SHORT)}}, {{TIME(2026-11-07T21:47:06Z)}}",
		"yesterday":                 "yesterday",
		"":                          "",
	}
	for in, want := range cases {
		if got := localTime(in); got != want {
			t.Errorf("localTime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRichActions_SkipEmptyAndInvalidURLs(t *testing.T) {
	msg := homerun.Message{URL: "https://x.example/pr/1", Tags: "run=https://ci.example/r/9,bad=javascript:alert(1)"}
	got := richActions(msg, []config.CardActionConfig{
		{Title: "PR", Field: "url"},
		{Title: "Diff", Template: "{field:url}/files"},
		{Title: "CI", Template: "{tag:run}"},
		{Title: "Missing", Template: "https://x.example/{tag:nope}"},
		{Title: "Bad", Template: "{tag:bad}"},
		{Title: "Nothing", Field: "assigneeaddress"},
	}, templateLookup(msg))
	var titles []string
	for _, a := range got {
		titles = append(titles, a.Title+"="+a.URL)
	}
	want := "PR=https://x.example/pr/1 Diff=https://x.example/pr/1/files CI=https://ci.example/r/9"
	if strings.Join(titles, " ") != want {
		t.Errorf("actions = %v, want %s", titles, want)
	}
	// No configured actions: "Open" for the url, none without one.
	if a := richActions(msg, nil, templateLookup(msg)); len(a) != 1 || a[0].Title != "Open" {
		t.Errorf("default actions = %+v", a)
	}
	if a := richActions(homerun.Message{}, nil, templateLookup(homerun.Message{})); len(a) != 0 {
		t.Errorf("actions without url = %+v", a)
	}
}

func TestRichCard_HeadlineFallbacks(t *testing.T) {
	cfg := config.CardConfig{Layout: "rich", Headline: config.Templates{"PR #{tag:number} {tag:action}", "{tag:repo}"}}
	cases := map[string]string{
		"number=1,action=merged,repo=r": "✅ PR #1 merged",
		"repo=r":                        "✅ r",
		"":                              "✅ the title",
	}
	for tags, want := range cases {
		card := buildCard(homerun.Message{Title: "the title", Severity: "success", Tags: tags}, "", cfg)
		if got := card.Body[0].Items[0].Text; got != want {
			t.Errorf("tags %q: headline %q, want %q", tags, got, want)
		}
	}
}

// bodyTexts lists the TextBlocks between the header and the FactSet.
func bodyTexts(card models.AdaptiveCard) []string {
	var out []string
	for _, e := range card.Body[1:] {
		if e.Type != "TextBlock" {
			break
		}
		out = append(out, e.Text)
	}
	return out
}

func TestRichCard_Text(t *testing.T) {
	msg := homerun.Message{
		Title: "PR #1: fix: x (merged) on r", Message: "PR merged by a: b → main", Severity: "success",
		Tags: "number=1,action=merged,title=fix: x",
	}
	head := config.Templates{"PR #{tag:number} {tag:action}"}
	cases := []struct {
		name string
		msg  homerun.Message
		text config.Templates
		want []string
	}{
		{"set, resolves", msg, config.Templates{"{tag:summary}", "{tag:title}"}, []string{"fix: x"}},
		{"set, none resolves", msg, config.Templates{"{tag:summary}"}, []string{msg.Title, msg.Message}},
		{"unset", msg, nil, []string{msg.Title, msg.Message}},
		{"resolved alert", homerun.Message{
			Title: "KubeJobFailed", Message: "Resolved: job failed", Tags: "status=resolved,alertname=KubeJobFailed",
		}, config.Templates{"{field:message}"}, []string{"Resolved: job failed"}},
		{"resolved alert, default body", homerun.Message{
			Title: "KubeJobFailed", Message: "Resolved: job failed", Tags: "status=resolved",
		}, config.Templates{"{tag:summary}"}, []string{"job failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := buildCard(tc.msg, "", config.CardConfig{Layout: "rich", Headline: head, Text: tc.text})
			if got := bodyTexts(card); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("body texts = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichCard_NoDetailsNoToggle(t *testing.T) {
	card := buildCard(homerun.Message{Title: "t"}, "", config.CardConfig{Layout: "rich"})
	for _, e := range card.Body {
		if e.ID == richDetailsID {
			t.Errorf("details container without tags or objectId: %+v", e)
		}
	}
	if len(card.Actions) != 0 {
		t.Errorf("actions = %+v", card.Actions)
	}
}

func TestRichCard_HiddenDetailsSerialiseIsVisibleFalse(t *testing.T) {
	env := BuildEnvelopeWithCard(models.CaughtMessage{Message: homerun.Message{Title: "t"}, ObjectID: "o"}, config.CardConfig{Layout: "Rich"})
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"id":"details","isVisible":false`) || !strings.Contains(string(b), `"version":"1.5"`) {
		t.Errorf("payload: %s", b)
	}
}
