package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadNotifyConfig_PlaceholderInCommentIsIgnored(t *testing.T) {
	// Regression for the env-interp footgun (issue #23): a ${VAR} reference in
	// a YAML comment used to be substituted as if it were a real placeholder,
	// crashing the catcher on startup when the named var was unset. After the
	// node-tree-walk refactor, comments are never visited.
	_ = os.Unsetenv("NEVER_SET_ANYWHERE")
	t.Setenv("TEAMS_WEBHOOK_URL", "https://teams.example/hook")
	p := writeTmp(t, `
outputs:
  - name: teams-alerts
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"   # or set ${NEVER_SET_ANYWHERE} for a dedicated secret
`)
	cfg, err := LoadNotifyConfig(p)
	if err != nil {
		t.Fatalf("comment-only placeholder should not error: %v", err)
	}
	if cfg.Outputs[0].WebhookURL != "https://teams.example/hook" {
		t.Errorf("real value placeholder should still resolve: %+v", cfg.Outputs[0])
	}
}

func TestLoadNotifyConfig_MissingVarsAreCollected(t *testing.T) {
	_ = os.Unsetenv("FOO_X")
	_ = os.Unsetenv("FOO_Y")
	t.Setenv("OK_VAR", "ok")

	p := writeTmp(t, `
outputs:
  - name: webhook-a
    type: webhook
    url: ${FOO_X}
    headers:
      X-Auth: ${FOO_Y}
      X-Tag: ${OK_VAR}
  - name: webhook-b
    type: webhook
    url: ${FOO_X}
`)
	_, err := LoadNotifyConfig(p)
	if err == nil {
		t.Fatal("expected error for unresolved vars")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FOO_X") || !strings.Contains(msg, "FOO_Y") {
		t.Errorf("error should list all distinct missing vars, got: %s", msg)
	}
	if strings.Contains(msg, "OK_VAR") {
		t.Errorf("resolvable vars should not appear in error, got: %s", msg)
	}
}

func TestLoadNotifyConfig_EmptyEnvValueIsMissing(t *testing.T) {
	t.Setenv("EMPTY_VAR", "")
	p := writeTmp(t, `
outputs:
  - name: w
    type: webhook
    url: ${EMPTY_VAR}
`)
	_, err := LoadNotifyConfig(p)
	if err == nil {
		t.Fatal("empty env value should be treated as missing")
	}
}

func TestLoadNotifyConfig_NonReferenceCharsLeftAlone(t *testing.T) {
	// $5 and $HOME (without curly braces) must not be touched.
	t.Setenv("HOME", "/this-should-be-ignored")
	p := writeTmp(t, `
outputs:
  - name: w
    type: webhook
    url: https://example/$5/path
    headers:
      X-Shell: $HOME
`)
	cfg, err := LoadNotifyConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Outputs[0].URL; got != "https://example/$5/path" {
		t.Errorf("non-${} reference in URL was rewritten, got %q", got)
	}
	if got := cfg.Outputs[0].Headers["X-Shell"]; got != "$HOME" {
		t.Errorf("non-${} reference in header was rewritten, got %q", got)
	}
}

func TestLoadNotifyConfig_PlaceholderInHeadersValueResolves(t *testing.T) {
	// Headers map values are a real use case for env-interpolated secrets
	// (auth tokens). Walking only struct-known fields could miss them — the
	// node-walker reaches every scalar regardless of nesting.
	t.Setenv("PD_TOKEN", "sek-r3t")
	p := writeTmp(t, `
outputs:
  - name: w
    type: webhook
    url: https://hooks.example/pd
    headers:
      Authorization: "Token token=${PD_TOKEN}"
`)
	cfg, err := LoadNotifyConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Outputs[0].Headers["Authorization"]; got != "Token token=sek-r3t" {
		t.Errorf("header value not interpolated: %q", got)
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

func TestValidate_CardFacts(t *testing.T) {
	teams := func(facts ...CardFactConfig) OutputConfig {
		return OutputConfig{Name: "x", Type: OutputTypeTeams, WebhookURL: "https://a", Card: CardConfig{Facts: facts}}
	}
	cases := []struct {
		name string
		out  OutputConfig
		want string // "" = valid
	}{
		{"tag", teams(CardFactConfig{Title: "Sets", Tag: "sets"}), ""},
		{"field", teams(CardFactConfig{Title: "Time", Field: "Timestamp"}), ""},
		{"no title", teams(CardFactConfig{Tag: "sets"}), "title is required"},
		{"neither", teams(CardFactConfig{Title: "Sets"}), "exactly one of tag or field"},
		{"both", teams(CardFactConfig{Title: "Sets", Tag: "sets", Field: "system"}), "exactly one of tag or field"},
		{"unknown field", teams(CardFactConfig{Title: "Host", Field: "hostname"}), "hostname"},
		{"webhook", OutputConfig{
			Name: "x", Type: OutputTypeWebhook, URL: "https://a",
			Card: CardConfig{Facts: []CardFactConfig{{Title: "Sets", Tag: "sets"}}},
		}, "only supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&NotifyConfig{Outputs: []OutputConfig{tc.out}})
			if tc.want == "" {
				if err != nil {
					t.Errorf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRetryFor(t *testing.T) {
	cfg := &NotifyConfig{
		Retry: RetryConfig{Attempts: 5},
		Outputs: []OutputConfig{
			{Name: "inherits"},
			{Name: "own", Retry: RetryConfig{Attempts: 1, Backoff: "250ms"}},
		},
	}
	if got := cfg.RetryFor(cfg.Outputs[0]); got != (RetryPolicy{Attempts: 5, Backoff: DefaultRetryBackoff}) {
		t.Errorf("inherits: %+v", got)
	}
	if got := cfg.RetryFor(cfg.Outputs[1]); got != (RetryPolicy{Attempts: 1, Backoff: 250 * time.Millisecond}) {
		t.Errorf("own: %+v", got)
	}
	if got := (&NotifyConfig{}).RetryFor(OutputConfig{}); got != (RetryPolicy{Attempts: DefaultRetryAttempts, Backoff: DefaultRetryBackoff}) {
		t.Errorf("defaults: %+v", got)
	}
}

func TestValidate_Retry(t *testing.T) {
	out := func(r RetryConfig) *NotifyConfig {
		return &NotifyConfig{Outputs: []OutputConfig{{Name: "x", Type: OutputTypeWebhook, URL: "https://a", Retry: r}}}
	}
	for _, tc := range []struct {
		cfg  *NotifyConfig
		want string // "" = valid
	}{
		{out(RetryConfig{Attempts: 3, Backoff: "2s"}), ""},
		{out(RetryConfig{Backoff: "0s"}), ""},
		{out(RetryConfig{Attempts: 11}), `output[0] "x": retry.attempts 11`},
		{out(RetryConfig{Attempts: -1}), "retry.attempts -1"},
		{out(RetryConfig{Backoff: "soon"}), `retry.backoff "soon"`},
		{out(RetryConfig{Backoff: "1m"}), `retry.backoff "1m"`},
		{&NotifyConfig{Retry: RetryConfig{Backoff: "-1s"}}, `retry.backoff "-1s"`},
	} {
		err := Validate(tc.cfg)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%+v: expected valid, got %v", tc.cfg, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("expected error containing %q, got %v", tc.want, err)
		}
	}
}

func TestLoadNotifyConfig_StatusFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
outputs:
  - name: resolved-only
    type: webhook
    url: https://a
    filters:
      match: {status: resolved}
  - name: firing-only
    type: webhook
    url: https://b
    filters:
      send_resolved: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadNotifyConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Outputs[0].Filters.Match["status"] != "resolved" {
		t.Errorf("match %v", cfg.Outputs[0].Filters.Match)
	}
	if sr := cfg.Outputs[1].Filters.SendResolved; sr == nil || *sr {
		t.Errorf("send_resolved %v, want false", sr)
	}
	if cfg.Outputs[0].Filters.SendResolved != nil {
		t.Error("send_resolved unset: want nil")
	}
}
