package contentvalidator

// This file is the deployment arming seam for the Layer-2 content-validation
// gate. The registry machinery (registry.go) is generic and ships inert; this
// is how an embedding deployment turns it on WITHOUT editing a cmd entrypoint
// on every SAGE release.

// Provider builds the ContentValidatorRegistry a deployment wants installed on
// every SageApp. It is invoked once per app construction, before the app starts
// processing blocks, so it must return a fully-populated registry and must not
// mutate it afterwards — the same boot-only / frozen-after-boot contract that
// RegisterContentValidator and RegisterClosedDomain require.
type Provider func() *ContentValidatorRegistry

// provider is the process-wide arming hook. SAGE ships it nil: no provider, no
// gate, every submission passes through exactly as on a build without this seam.
var provider Provider

// SetProvider installs the registry provider for this process. It is the
// documented, release-stable seam a deployment uses to arm the Layer-2
// content-validation gate without patching cmd/amid or cmd/sage-gui on every
// SAGE bump.
//
// The pattern mirrors database/sql driver registration: drop a single additive
// .go file into any package the binary already compiles (this package is always
// compiled by the ABCI app) and register from its init():
//
//	func init() {
//	    contentvalidator.SetProvider(func() *contentvalidator.ContentValidatorRegistry {
//	        r := contentvalidator.NewContentValidatorRegistry()
//	        r.RegisterContentValidator("my-domain", "my-class", myValidator)
//	        r.RegisterClosedDomain("my-domain")
//	        return r
//	    })
//	}
//
// Because SAGE registers no provider, a stock build keeps the gate inert. The
// ABCI constructors consult the provider automatically (see BuildFromProvider),
// so a deployment that registers one needs no other wiring and never re-patches
// the entrypoints.
//
// Boot-only: call from an init() or before constructing the ABCI app. Last
// registration wins; passing nil clears it (handy in tests). Not safe to call
// concurrently with app construction.
func SetProvider(p Provider) {
	provider = p
}

// HasProvider reports whether a provider is registered, so callers can log a
// clear "gate armed by deployment provider" line without building a registry.
func HasProvider() bool {
	return provider != nil
}

// BuildFromProvider returns a freshly built registry from the registered
// provider, or nil if none is registered. The ABCI constructors call this to
// auto-install the gate; a nil return leaves it inert (pass-through).
func BuildFromProvider() *ContentValidatorRegistry {
	if provider == nil {
		return nil
	}
	return provider()
}

// ---------------------------------------------------------------------------
// Context-aware arming seam
//
// The no-arg Provider above is perfect for STATELESS validators (the doc's
// example). Some deployments need live, read-only chain state at arm time: a
// signer-authority check can only trust a record's self-asserted role if the
// on-chain signer actually holds that role, which means the validator closure
// must capture a role lookup over chain state. The no-arg provider can't reach
// that state, forcing a deployment back onto per-release cmd-entrypoint patches
// (app.SetContentValidators(reg) wired with app.RoleResolver()).
//
// ProviderWithContext closes that gap: it receives an ArmContext exposing only
// the deterministic, read-only lookups the enforcement path ALREADY consumes
// inside FinalizeBlock, so an opt-in deployment arms a stateful gate from a
// single additive init() — no cmd edits, fully release-stable. It is purely
// additive: stock builds register nothing and the gate stays inert; the no-arg
// Provider keeps working unchanged.
// ---------------------------------------------------------------------------

// ArmContext exposes the deterministic, read-only chain state a context-aware
// provider may capture at arm time. It is intentionally narrow — only what the
// Layer-2 gate already reads during enforcement is surfaced here, so a context
// provider gains NO state the FinalizeBlock path doesn't already use, and the
// arming seam adds no new nondeterminism surface (no time, no network, no
// writes, no goroutines).
//
// RoleResolver returns the same per-call, read-only on-chain role lookup the
// gate consumes inside FinalizeBlock (the ABCI app's RoleResolver): given an
// agent ID it returns the RAW on-chain role, or "" when the agent is unknown or
// the read errors. A validator captures this closure ONCE at arm time and calls
// it per record to enforce signer authority — trusting a self-asserted role
// only when the on-chain signer actually holds it. Mapping the raw role string
// to a deployment's own role vocabulary is the deployment's concern.
type ArmContext interface {
	RoleResolver() func(agentID string) string
}

// ProviderWithContext builds the ContentValidatorRegistry a deployment wants
// installed, given read-only access to arm-time chain state. Same boot-only /
// frozen-after-boot contract as Provider: invoked once per app construction,
// before block processing; must return a fully-populated registry and must not
// mutate it afterwards.
type ProviderWithContext func(ArmContext) *ContentValidatorRegistry

// providerWithContext is the process-wide context-aware arming hook, nil by
// default (no provider, no gate) exactly like provider above.
var providerWithContext ProviderWithContext

// SetProviderWithContext installs the context-aware registry provider for this
// process. It is the release-stable seam a deployment uses to arm a STATEFUL
// Layer-2 gate (one whose validators need on-chain role lookups) from a single
// additive .go file, with no cmd-entrypoint patches:
//
//	func init() {
//	    contentvalidator.SetProviderWithContext(func(c contentvalidator.ArmContext) *contentvalidator.ContentValidatorRegistry {
//	        reg := contentvalidator.NewContentValidatorRegistry()
//	        sentinel.Register(reg, c.RoleResolver()) // signer-authority preserved
//	        return reg
//	    })
//	}
//
// Boot-only: call from an init() or before constructing the ABCI app. Last
// registration wins; passing nil clears it (handy in tests). Not safe to call
// concurrently with app construction. If BOTH a context-aware provider and a
// no-arg Provider are registered, the context-aware one takes precedence (it is
// the richer of the two); the ABCI constructor logs that case so a deployment
// never silently loses the no-arg registration.
func SetProviderWithContext(p ProviderWithContext) {
	providerWithContext = p
}

// HasProviderWithContext reports whether a context-aware provider is registered,
// so callers can log a clear "gate armed by context provider" line without
// building a registry.
func HasProviderWithContext() bool {
	return providerWithContext != nil
}

// BuildFromProviderWithContext returns a freshly built registry from the
// registered context-aware provider, passing it the supplied ArmContext, or nil
// if none is registered. The ABCI constructor calls this with an ArmContext
// backed by the app; a nil return leaves the gate inert (pass-through).
func BuildFromProviderWithContext(c ArmContext) *ContentValidatorRegistry {
	if providerWithContext == nil {
		return nil
	}
	return providerWithContext(c)
}
