package abci

import (
	"testing"

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
	contentvalidator.SetProvider(nil) // ensure no leakage from another test
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
