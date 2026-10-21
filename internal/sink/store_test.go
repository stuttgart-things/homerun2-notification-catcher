package sink

import (
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

func TestStoreRingBufferPerChannel(t *testing.T) {
	s := NewStore(3)
	env := notify.BuildEnvelope(caught(homerun.Message{Title: "x"}))
	for i := 0; i < 5; i++ {
		s.AddTeams("a", env, []byte(`{}`))
	}
	s.AddTeams("b", env, []byte(`{}`))

	a := s.List(Query{Channel: "a"})
	if len(a) != 3 {
		t.Fatalf("channel a keeps %d, want 3", len(a))
	}
	if a[0].Seq != 5 || a[2].Seq != 3 {
		t.Errorf("want newest first 5..3, got %d..%d", a[0].Seq, a[2].Seq)
	}
	if got := s.Channels(); got["msteams/a"] != 3 || got["msteams/b"] != 1 {
		t.Errorf("channels = %v", got)
	}
	if s.Get("1") != nil {
		t.Error("dropped post still retrievable")
	}
	if s.Get("6") == nil {
		t.Error("post 6 missing")
	}
}

func TestStoreQueryCursorAndLimit(t *testing.T) {
	s := NewStore(0)
	env := notify.BuildEnvelope(caught(homerun.Message{Title: "x"}))
	for i := 0; i < 4; i++ {
		s.AddTeams("a", env, []byte(`{}`))
	}
	if got := s.List(Query{AfterSeq: 2}); len(got) != 2 {
		t.Errorf("after 2: %d, want 2", len(got))
	}
	if got := s.List(Query{Limit: 1}); len(got) != 1 || got[0].Seq != 4 {
		t.Errorf("limit 1: %+v", got)
	}
	if got := s.List(Query{Since: time.Now().Add(time.Minute)}); len(got) != 0 {
		t.Errorf("since future: %d, want 0", len(got))
	}

	s.Clear("", "a")
	if got := s.List(Query{}); len(got) != 0 {
		t.Errorf("after clear: %d", len(got))
	}
}

func TestSummarizeDefaultCard(t *testing.T) {
	s := NewStore(0)
	m := s.AddTeams("a", notify.BuildEnvelope(caught(homerun.Message{
		Title: "boom", Message: "broke", Severity: "critical", Author: "me",
	})), []byte(`{}`))
	if m.Title != "boom" || m.Style != "attention" || m.Facts["Author"] != "me" || m.FallbackText != "[critical] boom: broke" || m.Severity != "critical" {
		t.Errorf("summary: %+v", m)
	}
}

func TestSummarizeSeverityFromFallbackText(t *testing.T) {
	// A card whose configured facts leave out Severity still gets it from
	// the "[severity] " prefix of its fallback text.
	env := notify.BuildEnvelopeWithCard(caught(homerun.Message{Title: "t", Severity: "warning", Tags: "k=v"}),
		config.CardConfig{Facts: []config.CardFactConfig{{Title: "K", Tag: "k"}}})
	m := NewStore(0).AddTeams("a", env, []byte(`{}`))
	if m.Severity != "warning" || m.Facts["K"] != "v" {
		t.Errorf("summary: %+v", m)
	}
}
