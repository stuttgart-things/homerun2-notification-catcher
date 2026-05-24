package catcher

// MockCatcher is a test double for the Catcher interface.
type MockCatcher struct {
	stop   chan struct{}
	errors chan error
}

func NewMockCatcher() *MockCatcher {
	return &MockCatcher{
		stop:   make(chan struct{}, 1),
		errors: make(chan error),
	}
}

func (m *MockCatcher) Run() {
	if m.stop == nil {
		m.stop = make(chan struct{}, 1)
	}
	<-m.stop
}

func (m *MockCatcher) Shutdown() {
	if m.stop == nil {
		m.stop = make(chan struct{}, 1)
	}
	m.stop <- struct{}{}
}

func (m *MockCatcher) Errors() <-chan error {
	if m.errors == nil {
		m.errors = make(chan error)
	}
	return m.errors
}
