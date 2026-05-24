package catcher

import "testing"

func TestMockCatcherImplementsInterface(t *testing.T) {
	var _ Catcher = (*MockCatcher)(nil)
}

func TestRedisCatcherImplementsInterface(t *testing.T) {
	var _ Catcher = (*RedisCatcher)(nil)
}
