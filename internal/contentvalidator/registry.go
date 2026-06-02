// Package contentvalidator provides a generic, deployment-agnostic Layer-2
// content-aware schema gate for memory submissions.
//
// A ContentValidator is a pure function of a single memory record: it inspects
// the record (typically its Content body) and returns nil to accept or a non-nil
// error to REJECT. The error string is surfaced verbatim as the on-chain reject
// Log, so it must be deterministic across all validators and free of any
// node-local or non-reproducible state.
//
// This package ships ZERO knowledge of any particular deployment, domain, or
// outcome schema. Validators are registered at boot by the embedding binary,
// keyed by (domain, outcomeClass). A node that registers nothing — or never
// activates the gate — behaves exactly as a node without this package: every
// submission passes through untouched.
package contentvalidator

import "github.com/l33tdawg/sage/internal/memory"

// ContentValidator is a pure function of a memory record. It returns nil to
// accept the record, or a non-nil error to reject it. The error string becomes
// the on-chain reject Log, so it MUST be deterministic across all validators.
type ContentValidator func(rec *memory.MemoryRecord) error

// validatorKey identifies a registered validator by the (domain, outcomeClass)
// pair it applies to. Both components are matched exactly.
type validatorKey struct {
	domain       string
	outcomeClass string
}

// ContentValidatorRegistry maps (domain, outcomeClass) pairs to the validator
// that gates submissions of that shape. The map is populated once at boot and
// is treated as read-only (frozen) for the lifetime of the process.
type ContentValidatorRegistry struct {
	m map[validatorKey]ContentValidator
}

// NewContentValidatorRegistry returns an empty registry. An empty registry is a
// pass-through: Validate accepts every key until a validator is registered.
func NewContentValidatorRegistry() *ContentValidatorRegistry {
	return &ContentValidatorRegistry{
		m: make(map[validatorKey]ContentValidator),
	}
}

// RegisterContentValidator binds a validator to a (domain, outcomeClass) pair.
//
// This is a boot-only operation. The map is frozen during consensus: callers
// must register every validator before the ABCI app begins processing blocks,
// and must never mutate the registry afterwards. Concurrent registration during
// FinalizeBlock would make the gate non-deterministic across validators.
func (r *ContentValidatorRegistry) RegisterContentValidator(domain, outcomeClass string, v ContentValidator) {
	r.m[validatorKey{domain: domain, outcomeClass: outcomeClass}] = v
}

// Validate runs the validator registered for (domain, outcomeClass), if any,
// against rec. It returns (rejected, reason): when rejected is true, reason is
// the validator's error string for the on-chain reject Log.
//
// Pass-through semantics (all return (false, "")):
//   - a nil receiver, or an empty registry;
//   - a key with NO registered validator (backward-compat: unknown shapes are
//     never rejected by this layer);
//   - a registered validator that returns nil.
//
// CRITICAL: pass-through is ONLY for keys with no registered validator. A
// REGISTERED key whose validator returns an error — including the case where
// the validator chokes on a malformed body — is a HARD REJECT, not a
// pass-through. Registering a validator is an explicit opt-in to gate that
// shape, so anything it cannot accept must be turned away.
func (r *ContentValidatorRegistry) Validate(domain, outcomeClass string, rec *memory.MemoryRecord) (bool, string) {
	if r == nil || len(r.m) == 0 {
		return false, ""
	}

	v, ok := r.m[validatorKey{domain: domain, outcomeClass: outcomeClass}]
	if !ok {
		return false, ""
	}

	if err := v(rec); err != nil {
		return true, err.Error()
	}
	return false, ""
}
