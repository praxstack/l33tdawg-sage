package abci

import "testing"

// TestIsSharedDomain pins the explicit shared-domain set and the prefix-based
// carve-out used to keep cross-cutting "meta" domain families (e.g. sage-*)
// from getting captured on first write after a chain reset.
func TestIsSharedDomain(t *testing.T) {
	cases := []struct {
		name   string
		domain string
		shared bool
	}{
		// Exact matches.
		{"general is shared", "general", true},
		{"self is shared", "self", true},
		{"meta is shared", "meta", true},

		// sage-* prefix family. These are SAGE-meta domains that are
		// network-wide-by-convention rather than single-owner.
		{"sage-debugging is shared", "sage-debugging", true},
		{"sage-development is shared", "sage-development", true},
		{"sage-rbac-debug is shared", "sage-rbac-debug", true},
		{"sage-architecture is shared", "sage-architecture", true},

		// Sibling spellings must NOT bleed into the prefix carve-out.
		{"sage (no dash) is owned", "sage", false},
		{"sageops (no dash) is owned", "sageops", false},

		// Per-project domains stay owned. levelup-* in particular has
		// real per-org ownership semantics; we deliberately don't carve
		// it out.
		{"levelup-bugs is owned", "levelup-bugs", false},
		{"levelup-deployment is owned", "levelup-deployment", false},
		{"calibration.sqli is owned", "calibration.sqli", false},
		{"rules.sqli is owned", "rules.sqli", false},
		{"random domain is owned", "some-project-domain", false},
		{"empty string is owned", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSharedDomain(tc.domain); got != tc.shared {
				t.Fatalf("isSharedDomain(%q) = %v, want %v", tc.domain, got, tc.shared)
			}
		})
	}
}
