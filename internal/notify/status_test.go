package notify

import (
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// resolvedAlert is a resolution as omni-pitcher's /pitch/alertmanager
// pitches it.
var resolvedAlert = homerun.Message{
	Title:     "KubePodCrashLooping",
	Message:   "Resolved: Pod is crash looping.",
	Severity:  "success",
	Author:    "alertmanager",
	System:    "msteams",
	Tags:      "status=resolved,fingerprint=c0ffee01,namespace=homerun2",
	Timestamp: "2026-10-22T08:30:00Z",
}

func TestAlertStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  homerun.Message
		want string
	}{
		{"firing tag", homerun.Message{Tags: "status=firing,fingerprint=x", Message: "Pod is crash looping."}, StatusFiring},
		{"resolved tag", resolvedAlert, StatusResolved},
		{"tag wins over the text", homerun.Message{Tags: "status=FIRING", Message: "Resolved: earlier"}, StatusFiring},
		{"/pitch/grafana resolution", homerun.Message{Message: "resolved: disk", Severity: "success"}, StatusResolved},
		{"other status tag", homerun.Message{Tags: "status=open"}, ""},
		{"no alert", homerun.Message{Title: "deploy done", Tags: "pull_request"}, ""},
		{"word in the text", homerun.Message{Message: "Issue resolved by #12"}, ""},
	} {
		if got := AlertStatus(tc.msg); got != tc.want {
			t.Errorf("%s: AlertStatus = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMatches_Status(t *testing.T) {
	firing := homerun.Message{Severity: "warning", Tags: "status=firing"}
	resolved := match(config.OutputFilters{Match: map[string]string{"status": "resolved"}})
	if !resolved(resolvedAlert) || resolved(firing) || resolved(homerun.Message{}) {
		t.Error("match status=resolved: want only the resolution")
	}
	if !match(config.OutputFilters{Match: map[string]string{"Status": "FIRING"}})(firing) {
		t.Error("match Status=FIRING: want the firing alert, case-insensitive")
	}
}

func TestMatches_SendResolved(t *testing.T) {
	no, yes := false, true
	plain := homerun.Message{Title: "deploy done"}
	drop := match(config.OutputFilters{SendResolved: &no})
	if drop(resolvedAlert) || !drop(plain) || !drop(homerun.Message{Tags: "status=firing"}) {
		t.Error("send_resolved false: want everything but the resolution")
	}
	if !match(config.OutputFilters{SendResolved: &yes})(resolvedAlert) || !match(config.OutputFilters{})(resolvedAlert) {
		t.Error("send_resolved true or unset: want the resolution")
	}
}

func match(f config.OutputFilters) func(homerun.Message) bool {
	return func(m homerun.Message) bool { return Matches(f, m) }
}

func TestBuildEnvelope_ResolvedCard(t *testing.T) {
	card := BuildEnvelope(models.CaughtMessage{Message: resolvedAlert}).Attachments[0].Content
	header := card.Body[0]
	if header.Style != "good" || header.Items[0].Color != "good" {
		t.Errorf("style %q color %q, want good", header.Style, header.Items[0].Color)
	}
	if got := header.Items[0].Text; got != "RESOLVED: KubePodCrashLooping" {
		t.Errorf("title %q", got)
	}
	if got := header.Items[1].Text; got != "Pod is crash looping." {
		t.Errorf("body %q: the title already says resolved", got)
	}
	if card.FallbackText != "[resolved] RESOLVED: KubePodCrashLooping: Pod is crash looping." {
		t.Errorf("fallback %q", card.FallbackText)
	}
	facts := map[string]string{}
	for _, f := range card.Body[1].Facts {
		facts[f.Title] = f.Value
	}
	if facts["Resolved"] != "2026-10-22T08:30:00Z" || facts["Time"] != "" {
		t.Errorf("facts %v: want the end time as Resolved", facts)
	}

	// A resolution of a critical alert, should a producer keep the severity:
	// green all the same, and the prefix is not doubled.
	crit := resolvedAlert
	crit.Severity, crit.Title = "critical", "RESOLVED: HostDiskUsage"
	h := BuildEnvelope(models.CaughtMessage{Message: crit}).Attachments[0].Content.Body[0]
	if h.Style != "good" || h.Items[0].Text != "RESOLVED: HostDiskUsage" {
		t.Errorf("critical resolution: style %q title %q", h.Style, h.Items[0].Text)
	}
}

func TestBuildEnvelope_FiringCardKeepsSeverity(t *testing.T) {
	msg := resolvedAlert
	msg.Severity, msg.Message, msg.Tags = "critical", "Pod is crash looping.", "status=firing,fingerprint=c0ffee01"
	card := BuildEnvelope(models.CaughtMessage{Message: msg}).Attachments[0].Content
	if h := card.Body[0]; h.Style != "attention" || h.Items[0].Text != "KubePodCrashLooping" {
		t.Errorf("firing: style %q title %q", h.Style, h.Items[0].Text)
	}
	if card.Body[1].Facts[len(card.Body[1].Facts)-1].Title != "Time" {
		t.Errorf("firing facts %v: want Time", card.Body[1].Facts)
	}
}
