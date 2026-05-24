package notify

import (
	"context"
	"errors"
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v3"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
)

// stubNotifier records calls and returns a canned error.
type stubNotifier struct {
	called int
	last   homerun.Message
	err    error
}

func (s *stubNotifier) Send(_ context.Context, msg homerun.Message) error {
	s.called++
	s.last = msg
	return s.err
}

func TestDispatcher_FanOutToMatchingOutputs(t *testing.T) {
	teams := &stubNotifier{}
	pd := &stubNotifier{}
	general := &stubNotifier{}

	d := NewDispatcherWithNotifiers(
		DispatcherEntry{
			Name:     "teams",
			Filters:  config.OutputFilters{SeverityMin: "warning"},
			Notifier: teams,
		},
		DispatcherEntry{
			Name:     "pagerduty",
			Filters:  config.OutputFilters{SeverityMin: "critical"},
			Notifier: pd,
		},
		DispatcherEntry{
			Name:     "general",
			Filters:  config.OutputFilters{}, // match-all
			Notifier: general,
		},
	)

	results := d.Dispatch(context.Background(), homerun.Message{
		Title:    "disk almost full",
		Severity: "warning",
	})

	// teams + general fire; pagerduty skipped (needs critical).
	if teams.called != 1 || general.called != 1 {
		t.Errorf("teams=%d general=%d want 1/1", teams.called, general.called)
	}
	if pd.called != 0 {
		t.Errorf("pagerduty should have been skipped (severity warning < critical), called=%d", pd.called)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[1].Output != "pagerduty" || !results[1].Skipped {
		t.Errorf("pagerduty result not flagged skipped: %+v", results[1])
	}
}

func TestDispatcher_OneOutputFailingDoesNotBlockOthers(t *testing.T) {
	bad := &stubNotifier{err: errors.New("upstream down")}
	good := &stubNotifier{}

	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "bad", Notifier: bad},
		DispatcherEntry{Name: "good", Notifier: good},
	)

	results := d.Dispatch(context.Background(), homerun.Message{Title: "x"})

	if bad.called != 1 || good.called != 1 {
		t.Errorf("both should be called: bad=%d good=%d", bad.called, good.called)
	}
	if results[0].Err == nil {
		t.Error("bad output result should carry the error")
	}
	if results[1].Err != nil {
		t.Errorf("good output should succeed, got err=%v", results[1].Err)
	}
}

func TestDispatcher_OutputNames(t *testing.T) {
	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "a"},
		DispatcherEntry{Name: "b"},
	)
	got := d.OutputNames()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("OutputNames = %v", got)
	}
}

func TestNewDispatcher_FromConfig(t *testing.T) {
	cfg := &config.NotifyConfig{Outputs: []config.OutputConfig{
		{Name: "teams", Type: config.OutputTypeTeams, WebhookURL: "https://teams"},
		{Name: "webhook", Type: config.OutputTypeWebhook, URL: "https://hook"},
	}}
	d, err := NewDispatcher(cfg, nil)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if names := d.OutputNames(); len(names) != 2 || names[0] != "teams" || names[1] != "webhook" {
		t.Errorf("OutputNames = %v", names)
	}
}

func TestNewDispatcher_NilConfig(t *testing.T) {
	if _, err := NewDispatcher(nil, nil); err == nil {
		t.Error("expected error on nil config")
	}
}
