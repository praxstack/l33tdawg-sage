package appvalidator

import (
	"bytes"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func testLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

func noopChecker(hash string) (bool, error) { return false, nil }

func dupChecker(hash string) (bool, error) { return true, nil }

func TestManagerKeyDerivation(t *testing.T) {
	seed1 := []byte("test-seed-one")
	seed2 := []byte("test-seed-two")

	m1a := NewManager(seed1, noopChecker, testLogger())
	m1b := NewManager(seed1, noopChecker, testLogger())
	m2 := NewManager(seed2, noopChecker, testLogger())

	// Same seed produces same keys
	keys1a := m1a.GetValidatorKeys()
	keys1b := m1b.GetValidatorKeys()
	for i := 0; i < 4; i++ {
		if !bytes.Equal(keys1a[i], keys1b[i]) {
			t.Errorf("same seed should produce same key for validator %d", i)
		}
	}

	// Different seed produces different keys
	keys2 := m2.GetValidatorKeys()
	for i := 0; i < 4; i++ {
		if bytes.Equal(keys1a[i], keys2[i]) {
			t.Errorf("different seed should produce different key for validator %d", i)
		}
	}
}

func TestManagerPreValidateAllAccept(t *testing.T) {
	m := NewManager([]byte("seed"), noopChecker, testLogger())

	accepted, results := m.PreValidate(
		"This is a substantive memory about ABCI consensus",
		"abc123", "general", "observation", 0.8,
	)

	if !accepted {
		t.Error("expected accepted=true for good memory")
	}
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Decision != "accept" {
			t.Errorf("validator %s: expected accept, got %s (%s)", r.ValidatorName, r.Decision, r.Reason)
		}
	}
}

func TestManagerPreValidateDedupReject(t *testing.T) {
	m := NewManager([]byte("seed"), dupChecker, testLogger())

	accepted, results := m.PreValidate(
		"This is a substantive memory about ABCI consensus",
		"abc123", "general", "observation", 0.8,
	)

	// 3 accept (sentinel, quality, consistency), 1 reject (dedup) → still quorum
	if !accepted {
		t.Error("expected accepted=true with 3/4 accept (dedup reject)")
	}

	rejectCount := 0
	for _, r := range results {
		if r.Decision == "reject" {
			rejectCount++
			if r.ValidatorName != "dedup" {
				t.Errorf("expected dedup to reject, got %s", r.ValidatorName)
			}
		}
	}
	if rejectCount != 1 {
		t.Errorf("expected exactly 1 reject, got %d", rejectCount)
	}
}

func TestManagerPreValidateQualityReject(t *testing.T) {
	m := NewManager([]byte("seed"), noopChecker, testLogger())

	// Content too short: quality rejects, others accept → 3/4 accept
	accepted, results := m.PreValidate(
		"too short", "abc123", "general", "observation", 0.8,
	)

	// sentinel=accept, dedup=accept, quality=reject, consistency=accept → 3/4 → accepted
	if !accepted {
		t.Error("expected accepted=true with 3/4 accept (quality reject for short content)")
	}

	found := false
	for _, r := range results {
		if r.ValidatorName == "quality" && r.Decision == "reject" {
			found = true
		}
	}
	if !found {
		t.Error("expected quality to reject short content")
	}
}

func TestManagerPreValidateMultiReject(t *testing.T) {
	m := NewManager([]byte("seed"), noopChecker, testLogger())

	// Too short AND empty domain: quality rejects + consistency rejects → only 2/4 accept
	accepted, results := m.PreValidate(
		"too short", "abc123", "", "observation", 0.8,
	)

	if accepted {
		t.Error("expected accepted=false with only 2/4 accept")
	}

	rejectCount := 0
	for _, r := range results {
		if r.Decision == "reject" {
			rejectCount++
		}
	}
	if rejectCount != 2 {
		t.Errorf("expected 2 rejects, got %d", rejectCount)
	}

	_ = results
}

func TestManagerGetValidatorIDs(t *testing.T) {
	m := NewManager([]byte("seed"), noopChecker, testLogger())
	ids := m.GetValidatorIDs()

	if len(ids) != 4 {
		t.Fatalf("expected 4 IDs, got %d", len(ids))
	}

	// All IDs should be unique
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		seen[id] = true

		// Each ID should be a valid hex-encoded sha256 (64 chars)
		if len(id) != 64 {
			t.Errorf("expected 64 char hex ID, got %d chars: %s", len(id), id)
		}
	}
}

func TestManagerQuorumThreshold(t *testing.T) {
	// Need exactly 3 accepts. Test with dup + low confidence (both reject) → only 2 accept → fail
	m := NewManager([]byte("seed"), dupChecker, testLogger())

	accepted, _ := m.PreValidate(
		"This is substantive content that passes quality checks",
		"abc123", "general", "observation", 0.1, // below 0.3 threshold
	)

	// sentinel=accept, dedup=reject(dup), quality=accept, consistency=reject(low conf) → 2/4 → fail
	if accepted {
		t.Error("expected accepted=false with only 2/4 accept")
	}

	// Now test with exactly 3: dup but valid otherwise
	m2 := NewManager([]byte("seed"), dupChecker, testLogger())
	accepted2, _ := m2.PreValidate(
		"This is substantive content that passes quality checks",
		"abc123", "general", "observation", 0.8,
	)

	// sentinel=accept, dedup=reject, quality=accept, consistency=accept → 3/4 → pass
	if !accepted2 {
		t.Error("expected accepted=true with 3/4 accept")
	}
}
