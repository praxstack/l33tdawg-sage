package contentvalidator

import (
	"errors"
	"testing"

	"github.com/l33tdawg/sage/internal/memory"
)

// resetProvider guarantees the process-wide provider hooks are nil before and
// after a test so registration never leaks across tests.
func resetProvider(t *testing.T) {
	t.Helper()
	provider = nil
	providerWithContext = nil
	t.Cleanup(func() { provider = nil; providerWithContext = nil })
}

// fakeArmContext is a test double for ArmContext backed by a static role map, so
// provider unit tests can exercise the captured resolver without an ABCI app.
type fakeArmContext struct{ roles map[string]string }

func (f fakeArmContext) RoleResolver() func(agentID string) string {
	return func(agentID string) string { return f.roles[agentID] }
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

// ---------------------------------------------------------------------------
// Context-aware provider
// ---------------------------------------------------------------------------

// TestProviderWithContext_DefaultIsNil pins the stock contract for the
// context-aware seam: no registration => HasProviderWithContext false and
// BuildFromProviderWithContext yields nil regardless of the context passed.
func TestProviderWithContext_DefaultIsNil(t *testing.T) {
	resetProvider(t)
	if HasProviderWithContext() {
		t.Fatal("expected no context provider by default")
	}
	if got := BuildFromProviderWithContext(fakeArmContext{}); got != nil {
		t.Fatalf("expected nil registry with no context provider, got %v", got)
	}
}

// TestProviderWithContext_CapturedResolverEnforcesSignerAuthority is the headline
// test: a context-aware provider captures the arm-time RoleResolver in a closure
// and the resulting validator enforces signer authority from on-chain role —
// rejecting a record whose signer does NOT hold the required role and accepting
// one whose signer does. This proves the seam delivers exactly what the no-arg
// Provider cannot: a stateful, forgery-resistant check wired with no cmd patch.
func TestProviderWithContext_CapturedResolverEnforcesSignerAuthority(t *testing.T) {
	resetProvider(t)

	armCtx := fakeArmContext{roles: map[string]string{
		"signer-authorized": "red-agent",
		"signer-imposter":   "member",
	}}

	SetProviderWithContext(func(c ArmContext) *ContentValidatorRegistry {
		resolve := c.RoleResolver() // captured ONCE at arm time
		r := NewContentValidatorRegistry()
		r.RegisterContentValidator("red", "detect", func(rec *memory.MemoryRecord) error {
			// Trust the self-asserted role only if the on-chain signer holds it.
			if resolve(rec.SubmittingAgent) != "red-agent" {
				return errors.New("rejected: signer lacks red-agent authority")
			}
			return nil
		})
		r.RegisterClosedDomain("red")
		return r
	})

	if !HasProviderWithContext() {
		t.Fatal("expected HasProviderWithContext true after SetProviderWithContext")
	}
	reg := BuildFromProviderWithContext(armCtx)
	if reg == nil {
		t.Fatal("expected non-nil registry from context provider")
	}

	if rejected, _ := reg.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: "signer-authorized"}); rejected {
		t.Fatal("expected accept when on-chain signer holds red-agent role")
	}
	if rejected, reason := reg.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: "signer-imposter"}); !rejected || reason == "" {
		t.Fatalf("expected reject when signer lacks authority, got rejected=%v reason=%q", rejected, reason)
	}
	if rejected, _ := reg.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: "signer-unknown"}); !rejected {
		t.Fatal("expected reject when signer is unknown on-chain (resolver returns \"\")")
	}
}

// TestProviderWithContext_BuildReturnsFreshRegistryEachCall locks the per-call
// build contract (each app construction gets its own registry), matching the
// no-arg provider's contract.
func TestProviderWithContext_BuildReturnsFreshRegistryEachCall(t *testing.T) {
	resetProvider(t)
	calls := 0
	SetProviderWithContext(func(c ArmContext) *ContentValidatorRegistry {
		calls++
		return NewContentValidatorRegistry()
	})
	_ = BuildFromProviderWithContext(fakeArmContext{})
	_ = BuildFromProviderWithContext(fakeArmContext{})
	if calls != 2 {
		t.Fatalf("expected context provider invoked once per build call, got %d", calls)
	}
}

// TestProviderWithContext_NilClears confirms SetProviderWithContext(nil) clears a
// previously registered context provider.
func TestProviderWithContext_NilClears(t *testing.T) {
	resetProvider(t)
	SetProviderWithContext(func(c ArmContext) *ContentValidatorRegistry { return NewContentValidatorRegistry() })
	SetProviderWithContext(nil)
	if HasProviderWithContext() {
		t.Fatal("SetProviderWithContext(nil) must clear the context provider")
	}
	if BuildFromProviderWithContext(fakeArmContext{}) != nil {
		t.Fatal("expected nil registry after clearing context provider")
	}
}

// TestProviderWithContext_IndependentFromNoArgProvider proves the two hooks are
// independent vars: registering one does not register or clear the other.
func TestProviderWithContext_IndependentFromNoArgProvider(t *testing.T) {
	resetProvider(t)
	SetProvider(func() *ContentValidatorRegistry { return NewContentValidatorRegistry() })
	if HasProviderWithContext() {
		t.Fatal("no-arg SetProvider must not register a context provider")
	}
	SetProviderWithContext(func(c ArmContext) *ContentValidatorRegistry { return NewContentValidatorRegistry() })
	if !HasProvider() {
		t.Fatal("registering a context provider must not clear the no-arg provider")
	}
}
