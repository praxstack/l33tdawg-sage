package contentvalidator

import (
	"errors"
	"testing"

	"github.com/l33tdawg/sage/internal/memory"
)

// resetProvider guarantees the process-wide provider hook is nil before and
// after a test so registration never leaks across tests.
func resetProvider(t *testing.T) {
	t.Helper()
	provider = nil
	t.Cleanup(func() { provider = nil })
}

// TestProvider_DefaultIsNil pins the stock-SAGE contract: with no provider
// registered, HasProvider is false and BuildFromProvider yields a nil registry
// (gate inert, every submission passes through).
func TestProvider_DefaultIsNil(t *testing.T) {
	resetProvider(t)
	if HasProvider() {
		t.Fatal("expected no provider by default")
	}
	if got := BuildFromProvider(); got != nil {
		t.Fatalf("expected nil registry with no provider, got %v", got)
	}
}

// TestProvider_SetAndBuild proves a registered provider builds a working
// registry: the registered validator rejects bad content, accepts good content,
// and the closed domain rejects an unregistered outcome_class.
func TestProvider_SetAndBuild(t *testing.T) {
	resetProvider(t)

	SetProvider(func() *ContentValidatorRegistry {
		r := NewContentValidatorRegistry()
		r.RegisterContentValidator("red", "detect", func(rec *memory.MemoryRecord) error {
			if rec.Content == "bad" {
				return errors.New("rejected: bad content")
			}
			return nil
		})
		r.RegisterClosedDomain("red")
		return r
	})

	if !HasProvider() {
		t.Fatal("expected HasProvider true after SetProvider")
	}
	reg := BuildFromProvider()
	if reg == nil {
		t.Fatal("expected non-nil registry from provider")
	}

	if rejected, reason := reg.Validate("red", "detect", &memory.MemoryRecord{Content: "bad"}); !rejected || reason == "" {
		t.Fatalf("expected reject for bad content, got rejected=%v reason=%q", rejected, reason)
	}
	if rejected, _ := reg.Validate("red", "detect", &memory.MemoryRecord{Content: "ok"}); rejected {
		t.Fatal("expected accept for ok content")
	}
	if rejected, _ := reg.Validate("red", "unknown", &memory.MemoryRecord{Content: "x"}); !rejected {
		t.Fatal("expected closed-domain reject for an unregistered class")
	}
}

// TestProvider_BuildReturnsFreshRegistryEachCall locks the contract that the
// provider is invoked per BuildFromProvider call (each app construction gets its
// own registry instance), not memoized.
func TestProvider_BuildReturnsFreshRegistryEachCall(t *testing.T) {
	resetProvider(t)
	calls := 0
	SetProvider(func() *ContentValidatorRegistry {
		calls++
		return NewContentValidatorRegistry()
	})
	_ = BuildFromProvider()
	_ = BuildFromProvider()
	if calls != 2 {
		t.Fatalf("expected provider invoked once per BuildFromProvider call, got %d", calls)
	}
}

// TestProvider_NilClears confirms SetProvider(nil) disables a previously
// registered provider (last registration wins; nil clears).
func TestProvider_NilClears(t *testing.T) {
	resetProvider(t)
	SetProvider(func() *ContentValidatorRegistry { return NewContentValidatorRegistry() })
	SetProvider(nil)
	if HasProvider() {
		t.Fatal("SetProvider(nil) must clear the provider")
	}
	if BuildFromProvider() != nil {
		t.Fatal("expected nil registry after clearing provider")
	}
}
