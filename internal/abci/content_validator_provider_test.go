package abci

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/contentvalidator"
	"github.com/l33tdawg/sage/internal/memory"
)

// TestConstructor_ArmsGateFromProvider proves the documented arming seam end to
// end: a deployment that registers a content-validator provider gets the gate
// auto-installed by the ABCI constructor, with NO SetContentValidators call and
// NO cmd-entrypoint patch. The process-wide provider is reset in cleanup so no
// other test in the package inherits an armed gate.
func TestConstructor_ArmsGateFromProvider(t *testing.T) {
	contentvalidator.SetProvider(func() *contentvalidator.ContentValidatorRegistry {
		r := contentvalidator.NewContentValidatorRegistry()
		r.RegisterContentValidator("prov-domain", "prov-class", func(rec *memory.MemoryRecord) error { return nil })
		return r
	})
	t.Cleanup(func() { contentvalidator.SetProvider(nil) })

	app := setupTestApp(t)
	if app.contentValidators == nil {
		t.Fatal("constructor must auto-install the gate when a provider is registered")
	}
}

// TestConstructor_NoProviderLeavesGateInert is the negative control: a stock
// build (no provider) constructs with the gate off, preserving the backward-
// compatible pass-through for every existing deployment.
func TestConstructor_NoProviderLeavesGateInert(t *testing.T) {
	// Defensively clear BOTH arming hooks so neither a leaked no-arg nor a leaked
	// context provider from another test arms the gate under us.
	contentvalidator.SetProvider(nil)
	contentvalidator.SetProviderWithContext(nil)
	app := setupTestApp(t)
	if app.contentValidators != nil {
		t.Fatal("stock build (no provider) must leave the content-validation gate inert")
	}
}

// TestConstructor_ExplicitRegistryNotOverriddenByProvider guards the precedence
// rule: an explicit SetContentValidators wins over a registered provider, so an
// operator/test that wires a specific registry is never silently overridden.
func TestConstructor_ExplicitRegistryNotOverriddenByProvider(t *testing.T) {
	contentvalidator.SetProvider(func() *contentvalidator.ContentValidatorRegistry {
		r := contentvalidator.NewContentValidatorRegistry()
		r.RegisterContentValidator("prov-domain", "prov-class", func(rec *memory.MemoryRecord) error { return nil })
		return r
	})
	t.Cleanup(func() { contentvalidator.SetProvider(nil) })

	explicit := contentvalidator.NewContentValidatorRegistry()
	app := setupTestApp(t)
	app.SetContentValidators(explicit)
	// Re-running the arming step must NOT replace the explicit registry.
	app.armContentValidatorsFromProvider()
	if app.contentValidators != explicit {
		t.Fatal("an explicitly wired registry must not be overridden by the provider")
	}
}

// TestConstructor_ArmsGateFromContextProvider_LiveRoleLookup is the integration
// headline: a context-aware provider, armed automatically by the ABCI
// constructor, captures the app's RoleResolver and enforces signer authority
// against REAL on-chain agent state — a forgery-resistant check the no-arg
// provider cannot make. No SetContentValidators call, no cmd-entrypoint patch.
func TestConstructor_ArmsGateFromContextProvider_LiveRoleLookup(t *testing.T) {
	contentvalidator.SetProviderWithContext(func(c contentvalidator.ArmContext) *contentvalidator.ContentValidatorRegistry {
		resolve := c.RoleResolver() // captured once at arm time
		r := contentvalidator.NewContentValidatorRegistry()
		r.RegisterContentValidator("red", "detect", func(rec *memory.MemoryRecord) error {
			if resolve(rec.SubmittingAgent) != "red-agent" {
				return errInsufficientSignerRole
			}
			return nil
		})
		r.RegisterClosedDomain("red")
		return r
	})
	t.Cleanup(func() { contentvalidator.SetProviderWithContext(nil) })

	app := setupTestApp(t)
	if app.contentValidators == nil {
		t.Fatal("constructor must auto-install the gate when a context provider is registered")
	}

	// Register agents on-chain AFTER construction: the resolver does a live
	// read-only Badger lookup per call, so it sees state registered any time
	// before enforcement — exactly the FinalizeBlock-time semantics.
	authorized := "00000000000000000000000000000000000000000000000000000000000000a1"
	imposter := "00000000000000000000000000000000000000000000000000000000000000b2"
	require.NoError(t, app.badgerStore.RegisterAgent(authorized, "red", "red-agent", "", "", "", 1))
	require.NoError(t, app.badgerStore.RegisterAgent(imposter, "blue", "member", "", "", "", 1))

	if rejected, _ := app.contentValidators.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: authorized}); rejected {
		t.Fatal("expected accept: on-chain signer holds red-agent role")
	}
	if rejected, _ := app.contentValidators.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: imposter}); !rejected {
		t.Fatal("expected reject: on-chain signer is a member, not red-agent (self-asserted role must not be trusted)")
	}
	unknown := "00000000000000000000000000000000000000000000000000000000000000c3"
	if rejected, _ := app.contentValidators.Validate("red", "detect", &memory.MemoryRecord{SubmittingAgent: unknown}); !rejected {
		t.Fatal("expected reject: signer unknown on-chain (resolver returns \"\")")
	}
}

// TestConstructor_ContextProviderTakesPrecedenceOverNoArg proves the documented
// precedence: when BOTH a context-aware and a no-arg provider are registered, the
// constructor installs the context-aware one.
func TestConstructor_ContextProviderTakesPrecedenceOverNoArg(t *testing.T) {
	contentvalidator.SetProvider(func() *contentvalidator.ContentValidatorRegistry {
		r := contentvalidator.NewContentValidatorRegistry()
		r.RegisterContentValidator("probe", "x", func(*memory.MemoryRecord) error { return errors.New("NOARG-REGISTRY") })
		return r
	})
	contentvalidator.SetProviderWithContext(func(c contentvalidator.ArmContext) *contentvalidator.ContentValidatorRegistry {
		r := contentvalidator.NewContentValidatorRegistry()
		r.RegisterContentValidator("probe", "x", func(*memory.MemoryRecord) error { return errors.New("CTX-REGISTRY") })
		return r
	})
	t.Cleanup(func() {
		contentvalidator.SetProvider(nil)
		contentvalidator.SetProviderWithContext(nil)
	})

	app := setupTestApp(t)
	if app.contentValidators == nil {
		t.Fatal("constructor must arm a gate when providers are registered")
	}
	// Both registries register (probe,x) with a distinctive reject reason. The
	// installed registry's reason reveals which provider won; the context-aware
	// one must take precedence.
	rejected, reason := app.contentValidators.Validate("probe", "x", &memory.MemoryRecord{})
	if !rejected {
		t.Fatal("expected the armed registry to reject the probe record")
	}
	if reason != "CTX-REGISTRY" {
		t.Fatalf("expected the context-aware registry to win, got reason %q", reason)
	}
}

// TestArmContext_DoesNotLeakApp guards the decoupling boundary: the ArmContext
// handed to a provider must NOT be the *SageApp, so a provider cannot downcast
// back into mutable app internals and quietly re-couple to the entrypoint.
func TestArmContext_DoesNotLeakApp(t *testing.T) {
	var sawApp, resolverWorks bool
	contentvalidator.SetProviderWithContext(func(c contentvalidator.ArmContext) *contentvalidator.ContentValidatorRegistry {
		_, sawApp = c.(*SageApp)
		resolverWorks = c.RoleResolver() != nil
		return contentvalidator.NewContentValidatorRegistry()
	})
	t.Cleanup(func() { contentvalidator.SetProviderWithContext(nil) })

	_ = setupTestApp(t)
	if sawApp {
		t.Fatal("ArmContext must not be assertable back to *SageApp (decoupling boundary breached)")
	}
	if !resolverWorks {
		t.Fatal("ArmContext.RoleResolver() must return a usable resolver")
	}
}

// errInsufficientSignerRole is a shared sentinel for the signer-authority test
// validators above.
var errInsufficientSignerRole = errors.New("rejected: signer lacks red-agent authority")
