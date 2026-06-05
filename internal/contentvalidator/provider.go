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
