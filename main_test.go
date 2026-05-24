package main

import (
	"testing"

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
