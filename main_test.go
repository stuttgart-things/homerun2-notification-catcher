package main

import (
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/catcher"
)

func TestCatcherInterface(t *testing.T) {
	var _ catcher.Catcher = &catcher.MockCatcher{}
}

func TestMockCatcherRunAndShutdown(t *testing.T) {
	mock := catcher.NewMockCatcher()

	done := make(chan struct{})
	go func() {
		mock.Run()
		close(done)
	}()

	mock.Shutdown()
	<-done
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":                     {},
		"24h":                  now.Add(-24 * time.Hour),
		"90m":                  now.Add(-90 * time.Minute),
		"2026-10-21T08:00:00Z": time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
	} {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("parseSince(yesterday): want an error")
	}
}
