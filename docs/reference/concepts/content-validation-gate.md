<!-- Verified against code at SAGE v10.1.1 (registry.go:67/84/109, app.go:897/932/1890/2123/2134). -->

# Layer-2 content-validation gate (and how a deployment arms it)

Verified against code at SAGE v10.1.1.

SAGE ships an optional, deployment-agnostic **content-validation gate**: a
consensus-path hook that can REJECT a memory submission based on the *shape of
its content*, keyed by `(domain, outcome_class)`. SAGE itself ships ZERO
validators — a stock build never rejects anything on this layer. A deployment
supplies its own validators and arms the gate without patching any SAGE source
it doesn't own.

This doc is the authoritative reference for the gate and, in particular, the
**arming seam** a downstream deployment uses so it never re-patches the `cmd`
entrypoints on a SAGE bump.

---

## What the gate is

A `ContentValidator` is a pure function of one record:

```go
type ContentValidator func(rec *memory.MemoryRecord) error // nil = accept, err = REJECT
```

The error string becomes the on-chain reject `Log`, so it MUST be deterministic
across every validator (no node-local or non-reproducible state) or validators
will diverge on `AppHash`.

Validators are held in a `ContentValidatorRegistry`, populated once at boot and
treated as frozen thereafter (`internal/contentvalidator/registry.go`):

- `RegisterContentValidator(domain, outcomeClass, v)` — bind a validator to an
  exact `(domain, outcome_class)` pair. (`registry.go:67`)
- `RegisterClosedDomain(domain)` — mark a domain closed-schema: once it has at
  least one validator, a submission whose `outcome_class` has NO registered
  validator is REJECTED instead of passing through. This closes the fail-open
  hole where a malformed/cross-class/router-miss body would commit unvalidated.
  (`registry.go:84`)
- `Validate(domain, outcomeClass, rec) (rejected, reason)` — the runtime call.
  A nil receiver or empty registry is pass-through. (`registry.go:109`)

The routing key is read from the submission body by `parseOutcomeClass`
(`internal/abci/app.go:1890`), which decodes ONLY the `outcome_class` field and
ignores every sibling.

### When enforcement is actually live

Enforcement requires BOTH (`internal/abci/app.go:2123`):

1. a registry is installed (`app.contentValidators != nil`), AND
2. the chain has activated the **app-v7** fork (`postAppV7Fork(height)` true).

There is no separate enable flag. A registry compiled in past the fork is enough;
enforcement is then chain-wide and driven by consensus state, not a per-node
toggle. A node on an app-v7 chain with NO registry stays bootable (a generic-only
fleet is valid) but emits an advisory — see `ContentValidationEnforcementWarning`
(`app.go:932`). A MIXED fleet (some nodes with a registry, some without) is the
real hazard: it forks `AppHash`. Every validator in a fleet must run the SAME
registry.

---

## Arming the gate (the extension point)

`internal/contentvalidator/provider.go` is the release-stable seam. A deployment
registers a `Provider` and the ABCI constructors install it automatically — no
`SetContentValidators` call site, no edit to `cmd/amid` or `cmd/sage-gui`.

```go
type Provider func() *ContentValidatorRegistry

func SetProvider(p Provider)                       // register (boot-only; nil clears)
func HasProvider() bool                            // is one registered?
func BuildFromProvider() *ContentValidatorRegistry // build one, or nil
```

Both constructors (`NewSageApp`, `NewSageAppWithStores`) call
`armContentValidatorsFromProvider` (`internal/abci/app.go`), which installs the
provider's registry **only if** no registry was already wired via
`SetContentValidators` (explicit wiring wins).

### Recommended pattern (zero cmd edits)

Mirror `database/sql` driver registration: drop ONE additive `.go` file into a
package the binary already compiles (this `contentvalidator` package always is)
and register from its `init()`:

```go
package contentvalidator // additive file, e.g. arming_mydeploy.go

func init() {
    SetProvider(func() *ContentValidatorRegistry {
        r := NewContentValidatorRegistry()
        r.RegisterContentValidator("my-domain", "my-class", myValidator)
        r.RegisterClosedDomain("my-domain")
        return r
    })
}
```

Because the file is *additive* (a new file, not an edit to an existing one), it
does not conflict on rebase across SAGE releases the way patching
`cmd/amid/main.go` + `cmd/sage-gui/node.go` did. The provider API is the stable
contract; the surrounding entrypoint code is free to move.

> If you prefer to keep validators in your own package, register from that
> package's `init()` and add a single blank import (`import _ ".../yourpkg"`) to
> a compiled package so the `init()` runs. Dropping the arming file directly into
> an already-compiled package avoids even that one import.

### Contract / gotchas

- **Boot-only.** Call `SetProvider` from an `init()` or before constructing the
  ABCI app. The registry is frozen once blocks are processing; mutating it (or
  registering concurrently with `FinalizeBlock`) is non-deterministic.
- **Determinism.** Every validator's accept/reject decision and error string must
  be identical across all validators. Non-determinism forks `AppHash`.
- **Whole-fleet parity.** Ship the same provider to every validator. A registry
  on some nodes but not others diverges state.
- **Stock SAGE registers nothing.** No provider ⇒ gate inert ⇒ identical to a
  build without this seam. Backward compatible by construction.
