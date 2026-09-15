package slskd

import (
	"testing"
	"time"

	"github.com/pxldi/schall/internal/sources"
)

func TestTheBuilderMakesAProviderFromStoredSettings(t *testing.T) {
	provider, err := Builder{}.Build(sources.Settings{
		BaseURL: "http://localhost:5030", APIKey: "key", SearchTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if provider.Name() != Name {
		t.Errorf("Name() = %q, want %q", provider.Name(), Name)
	}
	if want := defaultDequeueBudget + defaultCompletionGrace + 15*time.Second; provider.SearchBudget() != want {
		t.Errorf("SearchBudget() = %s, want %s: the dequeue wait, the reply window, "+
			"the wait for slskd to finish the search and the HTTP allowance",
			provider.SearchBudget(), want)
	}
}

func TestTheBuilderRefusesSettingsNoProviderCanBeMadeFrom(t *testing.T) {
	if _, err := (Builder{}).Build(sources.Settings{BaseURL: "http://localhost:5030"}); err == nil {
		t.Error("a provider was built with no API key")
	}
}

// A client is built afresh for every search, so a bound kept inside one would be
// no bound at all. Every client the builder makes shares the builder's gate.
func TestEveryProviderTheBuilderMakesSharesItsGate(t *testing.T) {
	gate := NewGate(2, time.Millisecond, time.Millisecond)
	builder := Builder{Gate: gate}

	first, err := builder.Build(sources.Settings{BaseURL: "http://localhost:5030", APIKey: "key"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	second, err := builder.Build(sources.Settings{BaseURL: "http://localhost:5030", APIKey: "key"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if first.(*Client).gate != gate || second.(*Client).gate != gate {
		t.Fatal("the providers do not share the builder's gate, so searching is not bounded")
	}
}

// A builder with no gate gives each client one of its own, so a caller that
// forgets is slower rather than throttled by slskd.
func TestAProviderBuiltWithoutAGateStillHasOne(t *testing.T) {
	provider, err := Builder{}.Build(sources.Settings{
		BaseURL: "http://localhost:5030", APIKey: "key",
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if provider.(*Client).gate == nil {
		t.Fatal("a provider was built with nothing bounding its searches")
	}
}
