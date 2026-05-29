package store

import (
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/poe"
)

// v8.4 per-domain validator stats + memory-domain key. The per-domain record
// reuses the v8.3 24/56-byte codec verbatim (golden-tested in
// badger_verdict_stats_test.go) — these tests pin the new key prefixes, the
// cold-start contract, cross-domain/global independence, atomicity, and the
// memdomain: accessors.

// DS1 — round-trip: a cold-start lookup reads as the EWMA prior, and
// UpdateDomainVerdictStats credits the per-domain EWMA + corroboration exactly
// like the global UpdateVerdictStats.
func TestDomainStats_DS1_RoundTrip(t *testing.T) {
	s := newTestBadger(t)
	const (
		vid = "v-ds1"
		dom = "pwn_heap"
	)

	// Cold start: no record yet → zero stats → EWMA cold-start prior 0.5.
	got, err := s.GetValidatorDomainStats(vid, dom)
	require.NoError(t, err)
	assert.Equal(t, &ValidatorStats{}, got, "absent per-domain record reads as zero")
	cold := &poe.EWMATracker{WeightedSum: got.EWMAWeightedSum, WeightDenom: got.EWMAWeightDenom, Count: int64(got.EWMACount)}
	assert.Equal(t, 0.5, cold.Accuracy(), "cold-start per-domain accuracy is the 0.5 prior")

	// Two matches then a miss.
	require.NoError(t, s.UpdateDomainVerdictStats(dom, map[string]bool{vid: true}))
	require.NoError(t, s.UpdateDomainVerdictStats(dom, map[string]bool{vid: true}))
	require.NoError(t, s.UpdateDomainVerdictStats(dom, map[string]bool{vid: false}))

	got, err = s.GetValidatorDomainStats(vid, dom)
	require.NoError(t, err)
	ref := poe.NewEWMATracker()
	ref.Update(1.0)
	ref.Update(1.0)
	ref.Update(0.0)
	assert.InDelta(t, ref.WeightedSum, got.EWMAWeightedSum, 1e-12)
	assert.InDelta(t, ref.WeightDenom, got.EWMAWeightDenom, 1e-12)
	assert.Equal(t, uint64(3), got.EWMACount)
	assert.Equal(t, uint64(2), got.CorrCount, "two matches → corr 2")
}

// DS2 — independence: per-domain records are keyed by (validator, domain) and
// are independent of each other AND of the global vstats: record. Crediting
// pwn_heap must not touch crypto or the global stats.
func TestDomainStats_DS2_Independence(t *testing.T) {
	s := newTestBadger(t)
	const vid = "v-ds2"

	// Global stats credited once (separate accumulator).
	require.NoError(t, s.UpdateVerdictStats(map[string]bool{vid: true}))
	// pwn_heap credited three times correct; crypto never.
	for i := 0; i < 3; i++ {
		require.NoError(t, s.UpdateDomainVerdictStats("pwn_heap", map[string]bool{vid: true}))
	}

	pwn, err := s.GetValidatorDomainStats(vid, "pwn_heap")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), pwn.EWMACount, "pwn_heap accrued its own history")
	assert.Equal(t, uint64(3), pwn.CorrCount)

	crypto, err := s.GetValidatorDomainStats(vid, "crypto")
	require.NoError(t, err)
	assert.Equal(t, &ValidatorStats{}, crypto, "crypto untouched → cold start")

	global, err := s.GetValidatorStats(vid)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), global.EWMACount, "global vstats: is a distinct accumulator")
	assert.Equal(t, uint64(1), global.CorrCount)
}

// DS3 — memdomain: set/get, and a missing key reads as "" (not an error) so the
// quorum treats legacy/unknown memories as "fall back to scalar weight".
func TestDomainStats_DS3_MemoryDomain(t *testing.T) {
	s := newTestBadger(t)

	missing, err := s.GetMemoryDomain("never-written")
	require.NoError(t, err)
	assert.Equal(t, "", missing, "absent memdomain: key → empty, no error")

	require.NoError(t, s.SetMemoryDomain("mem-x", "pwn_heap"))
	got, err := s.GetMemoryDomain("mem-x")
	require.NoError(t, err)
	assert.Equal(t, "pwn_heap", got)

	// Overwrite (e.g. a re-submit) is last-writer-wins.
	require.NoError(t, s.SetMemoryDomain("mem-x", "crypto"))
	got, err = s.GetMemoryDomain("mem-x")
	require.NoError(t, err)
	assert.Equal(t, "crypto", got)
}

// DS4 — atomicity: a batch error leaves no per-domain record changed. Mirrors
// the global V5 test — plant a corrupt 40-byte record for one validator and
// confirm the clean validator's record is unchanged when the db.Update rolls back.
func TestDomainStats_DS4_Atomic(t *testing.T) {
	s := newTestBadger(t)
	const dom = "pwn_heap"

	require.NoError(t, s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(validatorDomainStatsKey("v-a", dom), encodeValidatorStats(&ValidatorStats{TotalVotes: 1}, true)); err != nil {
			return err
		}
		return txn.Set(validatorDomainStatsKey("v-b", dom), make([]byte, 40))
	}))

	before, err := s.GetValidatorDomainStats("v-a", dom)
	require.NoError(t, err)

	// Sorted iteration hits v-a (ok) then v-b (decode error) → batch aborts.
	err = s.UpdateDomainVerdictStats(dom, map[string]bool{"v-a": true, "v-b": true})
	require.Error(t, err)

	after, err := s.GetValidatorDomainStats("v-a", dom)
	require.NoError(t, err)
	assert.Equal(t, before, after, "v-a must be unchanged when the batch rolls back")
}
