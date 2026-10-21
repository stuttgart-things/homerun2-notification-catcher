package notify

import (
	"context"
	"errors"
	"strings"
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// stubNotifier records calls and returns a canned error.
type stubNotifier struct {
	called int
	last   homerun.Message
	err    error
}

func (s *stubNotifier) Send(_ context.Context, msg models.CaughtMessage) error {
	s.called++
	s.last = msg.Message
	return s.err
}

// caught wraps msg as the catcher hands it to the notifiers, without an
// objectId.
func caught(msg homerun.Message) models.CaughtMessage {
	return models.CaughtMessage{Message: msg}
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

	results := d.Dispatch(context.Background(), caught(homerun.Message{
		Title:    "disk almost full",
		Severity: "warning",
	}))

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

	results := d.Dispatch(context.Background(), caught(homerun.Message{Title: "x"}))

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

func TestDispatcher_DryRun_SkipsSendForMatchingOutputs(t *testing.T) {
	teams := &stubNotifier{}
	pd := &stubNotifier{}

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
	)
	d.DryRun = true

	results := d.Dispatch(context.Background(), caught(homerun.Message{
		Title:    "disk almost full",
		Severity: "warning",
	}))

	if teams.called != 0 {
		t.Errorf("teams.Send should not be called in dry-run, called=%d", teams.called)
	}
	if pd.called != 0 {
		t.Errorf("pd.Send should not be called in dry-run (also filter-skipped), called=%d", pd.called)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// teams: filters matched → dry-run reported
	if !results[0].DryRun || results[0].Skipped || results[0].Err != nil {
		t.Errorf("teams result wrong: %+v", results[0])
	}
	// pagerduty: filters didn't match → still skipped
	if results[1].DryRun || !results[1].Skipped {
		t.Errorf("pagerduty should be skipped (filter), got: %+v", results[1])
	}
}

func TestDispatcher_DryRun_FailingNotifierIsNotInvoked(t *testing.T) {
	// A would-be-failing notifier should never be called in dry-run, so the
	// caller can't tell anything is broken downstream. That's by design —
	// dry-run is for verifying routing/filters, not the downstream service.
	bad := &stubNotifier{err: errors.New("would have failed")}

	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "bad", Notifier: bad},
	)
	d.DryRun = true

	results := d.Dispatch(context.Background(), caught(homerun.Message{Title: "x"}))

	if bad.called != 0 {
		t.Errorf("Notifier.Send must not be invoked in dry-run, called=%d", bad.called)
	}
	if len(results) != 1 || !results[0].DryRun || results[0].Err != nil {
		t.Errorf("expected dry-run result with no error, got %+v", results)
	}
}

func TestDispatcher_DryRun_DefaultIsFalse(t *testing.T) {
	// New dispatchers should default to live, not dry-run — otherwise
	// forgetting to set the flag would silently drop messages.
	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "live", Notifier: &stubNotifier{}},
	)
	if d.DryRun {
		t.Error("Dispatcher.DryRun should default to false")
	}
}

// OnDispatch gets every message with each output's result (#22, #49).
func TestDispatcher_OnDispatch(t *testing.T) {
	failing := &stubNotifier{err: errors.New("boom")}
	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "sent", Notifier: &stubNotifier{}},
		DispatcherEntry{Name: "failed", Notifier: failing},
		DispatcherEntry{Name: "filtered", Filters: config.OutputFilters{SeverityMin: "critical"}, Notifier: &stubNotifier{}},
	)
	var got []Record
	d.OnDispatch = func(r Record) { got = append(got, r) }
	d.Dispatch(context.Background(), models.CaughtMessage{Message: homerun.Message{Title: "x", Severity: "info"}, ObjectID: "1-0"})

	if len(got) != 1 || got[0].Message.ObjectID != "1-0" || got[0].At.IsZero() {
		t.Fatalf("records: %+v", got)
	}
	var results []string
	for _, r := range got[0].Results {
		results = append(results, r.Output+"="+r.Result())
	}
	if strings.Join(results, ",") != "sent=sent,failed=failed,filtered=filtered" {
		t.Errorf("results: %v", results)
	}

	d.DryRun = true
	d.Dispatch(context.Background(), caught(homerun.Message{Title: "y", Severity: "info"}))
	if r := got[1].Results[0].Result(); r != ResultDryRun {
		t.Errorf("dry run: %s", r)
	}
}
