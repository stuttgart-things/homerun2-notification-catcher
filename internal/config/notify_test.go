package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolateEnv_Replaces(t *testing.T) {
	t.Setenv("TEAMS_WEBHOOK_URL", "https://example/hook")
	t.Setenv("PD_KEY", "abc123")

	in := "url: ${TEAMS_WEBHOOK_URL}\nkey: ${PD_KEY}\n"
	out, err := interpolateEnv(in)
	if err != nil {
		t.Fatalf("interp: %v", err)
	}
	want := "url: https://example/hook\nkey: abc123\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestInterpolateEnv_MissingVarsAreCollected(t *testing.T) {
	_ = os.Unsetenv("FOO_X")
	_ = os.Unsetenv("FOO_Y")

	_, err := interpolateEnv("a: ${FOO_X}\nb: ${FOO_Y}\nc: ${FOO_X}\n")
	if err == nil {
		t.Fatal("expected error for unresolved vars")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FOO_X") || !strings.Contains(msg, "FOO_Y") {
		t.Errorf("error should list missing vars, got: %s", msg)
	}
}

func TestInterpolateEnv_EmptyValueIsMissing(t *testing.T) {
	t.Setenv("EMPTY_VAR", "")
	_, err := interpolateEnv("x: ${EMPTY_VAR}\n")
	if err == nil {
		t.Fatal("empty env value should be treated as missing")
	}
}

func TestInterpolateEnv_NonReferenceCharsLeftAlone(t *testing.T) {
	// $5 and $variable (no braces) must not be touched.
	in := "price: $5\nshell: $HOME\n"
	out, err := interpolateEnv(in)
	if err != nil {
		t.Fatalf("interp: %v", err)
	}
	if out != in {
		t.Errorf("non-${} references should be left alone, got %q", out)
	}
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadNotifyConfig_Valid(t *testing.T) {
	t.Setenv("TEAMS_WEBHOOK_URL", "https://teams.example/hook")
	p := writeTmp(t, `
outputs:
  - name: teams-alerts
    type: msteams
    webhook_url: ${TEAMS_WEBHOOK_URL}
    filters:
      severity_min: warning
      match:
        system: kubernetes
      tags_contain: [infra]
      message_contains: [disk]

  - name: webhook-pd
    type: webhook
    url: https://hooks.example/pd
    method: POST
    headers:
      Authorization: Bearer 123
    filters:
      severity_min: critical
`)
	cfg, err := LoadNotifyConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(cfg.Outputs))
	}
	if cfg.Outputs[0].WebhookURL != "https://teams.example/hook" {
		t.Errorf("env interpolation didn't run: %+v", cfg.Outputs[0])
	}
}

func TestLoadNotifyConfig_FileMissing(t *testing.T) {
	_, err := LoadNotifyConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("missing file should error")
	}
}

func TestValidate_RejectsUnknownType(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{Name: "x", Type: "carrier-pigeon"},
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("expected unknown-type error, got %v", err)
	}
}

func TestValidate_RejectsMissingTeamsURL(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{Name: "x", Type: OutputTypeTeams},
	}})
	if err == nil || !strings.Contains(err.Error(), "webhook_url") {
		t.Errorf("expected webhook_url error, got %v", err)
	}
}

func TestValidate_RejectsMissingWebhookURL(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{Name: "x", Type: OutputTypeWebhook},
	}})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url-required error, got %v", err)
	}
}

func TestValidate_RejectsDuplicateName(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{Name: "dup", Type: OutputTypeWebhook, URL: "https://a"},
		{Name: "dup", Type: OutputTypeWebhook, URL: "https://b"},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}

func TestValidate_RejectsUnknownSeverityMin(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{
			Name: "x", Type: OutputTypeWebhook, URL: "https://a",
			Filters: OutputFilters{SeverityMin: "fatal"},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "severity_min") {
		t.Errorf("expected severity_min error, got %v", err)
	}
}

func TestValidate_RejectsUnknownMatchKey(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{
			Name: "x", Type: OutputTypeWebhook, URL: "https://a",
			Filters: OutputFilters{Match: map[string]string{"hostname": "node01"}},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "hostname") {
		t.Errorf("expected unknown-match-key error, got %v", err)
	}
}

func TestValidate_AcceptsEmptyFilters(t *testing.T) {
	err := Validate(&NotifyConfig{Outputs: []OutputConfig{
		{Name: "x", Type: OutputTypeWebhook, URL: "https://a"},
	}})
	if err != nil {
		t.Errorf("empty filters should be valid (= match-all), got %v", err)
	}
}

func TestSeverityRank(t *testing.T) {
	cases := []struct {
		in    string
		rank  int
		known bool
	}{
		{"debug", 0, true},
		{"info", 1, true},
		{"success", 2, true},
		{"warning", 3, true},
		{"critical", 4, true},
		{"error", 4, true},
		{"  ERROR  ", 4, true},
		{"", 0, false},
		{"fatal", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			r, ok := severityRank(tc.in)
			if ok != tc.known {
				t.Errorf("known mismatch: got %v want %v", ok, tc.known)
			}
			if ok && r != tc.rank {
				t.Errorf("rank = %d want %d", r, tc.rank)
			}
		})
	}
}
