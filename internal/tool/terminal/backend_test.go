package terminal

import "testing"

func TestAsTerminalBackendCommandLifecycle(t *testing.T) {
	backend := &fakeBackend{}

	lifecycle := AsTerminalBackendCommandLifecycle(backend)
	if lifecycle == nil {
		t.Fatal("expected lifecycle capability")
	}

	if AsTerminalBackendCommandLifecycle(NewPTYBackend("/tmp")) != nil {
		t.Fatal("expected PTY placeholder backend to expose no lifecycle capability")
	}
}

func TestAsTerminalBackendMetadata(t *testing.T) {
	backend := &fakeBackend{}

	metadata := AsTerminalBackendMetadata(backend)
	if metadata == nil {
		t.Fatal("expected metadata capability")
	}

	if AsTerminalBackendMetadata(NewPTYBackend("/tmp")) != nil {
		t.Fatal("expected PTY placeholder backend to expose no metadata capability")
	}
}
