package context

import (
	"testing"

	"github.com/wen/opentalon/internal/types"
)

func TestTokenUsageTotal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		usage      types.TokenUsage
		wantTotal  int
	}{
		{"zero", types.TokenUsage{}, 0},
		{"prompt only", types.TokenUsage{PromptTokens: 100}, 100},
		{"both", types.TokenUsage{PromptTokens: 200, CompletionTokens: 50}, 250},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.Total(); got != tt.wantTotal {
				t.Errorf("Total() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}

func TestTokenTrackerRecord(t *testing.T) {
	t.Parallel()

	tracker := NewTokenTracker()
	tracker.Record(types.TokenUsage{PromptTokens: 100, CompletionTokens: 20})

	last := tracker.LastUsage()
	if last.PromptTokens != 100 || last.CompletionTokens != 20 {
		t.Fatalf("LastUsage = %+v, want {100, 20}", last)
	}

	total := tracker.TotalUsage()
	if total.PromptTokens != 100 || total.CompletionTokens != 20 {
		t.Fatalf("TotalUsage after first record = +%v, want {100, 20}", total)
	}

	tracker.Record(types.TokenUsage{PromptTokens: 300, CompletionTokens: 40})

	last = tracker.LastUsage()
	if last.PromptTokens != 300 || last.CompletionTokens != 40 {
		t.Fatalf("LastUsage after second record = %+v, want {300, 40}", last)
	}

	total = tracker.TotalUsage()
	if total.PromptTokens != 400 || total.CompletionTokens != 60 {
		t.Fatalf("TotalUsage after second record = %+v, want {400, 60}", total)
	}
}

func TestTokenTrackerActiveContextTokens(t *testing.T) {
	t.Parallel()

	tracker := NewTokenTracker()

	if got := tracker.ActiveContextTokens(); got != 0 {
		t.Fatalf("ActiveContextTokens before any record = %d, want 0", got)
	}

	tracker.Record(types.TokenUsage{PromptTokens: 500, CompletionTokens: 80})
	if got := tracker.ActiveContextTokens(); got != 580 {
		t.Fatalf("ActiveContextTokens after record = %d, want 580", got)
	}
}

func TestTokenTrackerContextWindowLimit(t *testing.T) {
	t.Parallel()

	tracker := NewTokenTracker()
	if got := tracker.ContextWindowLimit(); got != 0 {
		t.Fatalf("ContextWindowLimit default = %d, want 0", got)
	}

	tracker.SetContextWindowLimit(8192)
	if got := tracker.ContextWindowLimit(); got != 8192 {
		t.Fatalf("ContextWindowLimit after set = %d, want 8192", got)
	}
}

func TestTokenTrackerNilSafe(t *testing.T) {
	t.Parallel()

	var tracker *TokenTracker

	// All methods should be safe on nil receiver
	tracker.Record(types.TokenUsage{PromptTokens: 100})
	_ = tracker.LastUsage()
	_ = tracker.TotalUsage()
	_ = tracker.ContextWindowLimit()
	tracker.SetContextWindowLimit(4096)
	if got := tracker.ActiveContextTokens(); got != 0 {
		t.Fatalf("ActiveContextTokens on nil tracker = %d, want 0", got)
	}
}

func TestTokenTrackerConcurrentAccess(t *testing.T) {
	t.Parallel()

	tracker := NewTokenTracker()
	done := make(chan struct{})

	// Writer goroutine
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			tracker.Record(types.TokenUsage{PromptTokens: 1, CompletionTokens: 1})
		}
	}()

	// Reader goroutine: should not panic or race
	for i := 0; i < 100; i++ {
		_ = tracker.LastUsage()
		_ = tracker.TotalUsage()
		_ = tracker.ActiveContextTokens()
	}

	<-done

	total := tracker.TotalUsage()
	if total.PromptTokens != 100 || total.CompletionTokens != 100 {
		t.Fatalf("TotalUsage after concurrent writes = %+v, want {100, 100}", total)
	}
}
