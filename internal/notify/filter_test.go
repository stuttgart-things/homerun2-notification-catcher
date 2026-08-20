package notify

import (
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
)

func TestMatches_EmptyFiltersMatchEverything(t *testing.T) {
	if !Matches(config.OutputFilters{}, homerun.Message{Severity: "info"}) {
		t.Error("empty filters should match every message")
	}
}

func TestMatches_SeverityMin(t *testing.T) {
	cases := []struct {
		min, sev string
		want     bool
	}{
		{"warning", "critical", true},
		{"warning", "error", true},
		{"warning", "warning", true},
		{"warning", "info", false},
		{"warning", "debug", false},
		{"info", "success", true},
		{"critical", "warning", false},
		{"critical", "error", true},
		{"error", "critical", true},
		// unknown severity on the message is treated as info, so passes "info" but not "warning"
		{"info", "weird", true},
		{"warning", "weird", false},
	}
	for _, tc := range cases {
		got := Matches(
			config.OutputFilters{SeverityMin: tc.min},
			homerun.Message{Severity: tc.sev},
		)
		if got != tc.want {
			t.Errorf("min=%s sev=%s: got %v want %v", tc.min, tc.sev, got, tc.want)
		}
	}
}

func TestMatches_MatchMapIsExactAndCaseInsensitive(t *testing.T) {
	msg := homerun.Message{System: "Kubernetes", Author: "alertmanager", Severity: "warning"}

	if !Matches(
		config.OutputFilters{Match: map[string]string{"system": "kubernetes"}},
		msg,
	) {
		t.Error("system match should be case-insensitive")
	}
	if Matches(
		config.OutputFilters{Match: map[string]string{"system": "kubernet"}},
		msg,
	) {
		t.Error("match should be exact, not prefix")
	}
	if !Matches(
		config.OutputFilters{Match: map[string]string{"System": "Kubernetes", "author": "ALERTMANAGER"}},
		msg,
	) {
		t.Error("multiple match keys should AND, case-insensitive on key + value")
	}
}

func TestMatches_TagsContainIsOR(t *testing.T) {
	msg := homerun.Message{Tags: "infra,storage,disk"}

	if !Matches(
		config.OutputFilters{TagsContain: []string{"compute", "infra"}},
		msg,
	) {
		t.Error("any-substring should match infra")
	}
	if Matches(
		config.OutputFilters{TagsContain: []string{"compute", "network"}},
		msg,
	) {
		t.Error("no needle present → should not match")
	}
	if !Matches(
		config.OutputFilters{TagsContain: []string{"INFRA"}},
		msg,
	) {
		t.Error("substring match should be case-insensitive")
	}
}

func TestMatches_MessageContains(t *testing.T) {
	msg := homerun.Message{Message: "node01 OOM on container redis"}

	if !Matches(
		config.OutputFilters{MessageContains: []string{"disk", "OOM"}},
		msg,
	) {
		t.Error("OOM substring should match")
	}
	if Matches(
		config.OutputFilters{MessageContains: []string{"disk", "cpu"}},
		msg,
	) {
		t.Error("no needle present → should not match")
	}
}

func TestMatches_AllFiltersANDed(t *testing.T) {
	msg := homerun.Message{
		Severity: "warning",
		System:   "kubernetes",
		Tags:     "infra,storage",
		Message:  "disk almost full on node01",
	}
	f := config.OutputFilters{
		SeverityMin:     "warning",
		Match:           map[string]string{"system": "kubernetes"},
		TagsContain:     []string{"infra"},
		MessageContains: []string{"disk"},
	}
	if !Matches(f, msg) {
		t.Error("all rules satisfied → should match")
	}

	// Drop the right severity → no match
	msg2 := msg
	msg2.Severity = "info"
	if Matches(f, msg2) {
		t.Error("severity drops below floor → should not match")
	}

	// Wrong system → no match
	msg3 := msg
	msg3.System = "openshift"
	if Matches(f, msg3) {
		t.Error("system mismatch → should not match")
	}
}
