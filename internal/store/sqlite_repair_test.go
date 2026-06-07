package store

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/memory"
)

// TestFindByContentHash_CommittedOnly pins the dedup predicate to committed
// memories ONLY. The voter's dedupCheck runs while the candidate memory is
// itself in the table with status='proposed' — the old predicate
// (status != 'deprecated') matched that row, so every memory was rejected as a
// "duplicate" of itself and single-validator chains deprecated everything on
// arrival.
func TestFindByContentHash_CommittedOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := testMemory("m1", "agent1", "a perfectly unique observation", "general")
	require.NoError(t, s.InsertMemory(ctx, rec))
	hash := hex.EncodeToString(rec.ContentHash)

	// Proposed (the memory's own row) must NOT count as a duplicate.
	exists, err := s.FindByContentHash(ctx, hash)
	require.NoError(t, err)
	assert.False(t, exists, "a proposed memory must not be a duplicate of itself")

	// Once committed, the same content IS a duplicate.
	require.NoError(t, s.UpdateStatus(ctx, "m1", memory.StatusCommitted, time.Now().UTC()))
	exists, err = s.FindByContentHash(ctx, hash)
	require.NoError(t, err)
	assert.True(t, exists, "committed content must register as a duplicate")

	// Deprecated content is fair game to re-propose.
	require.NoError(t, s.UpdateStatus(ctx, "m1", memory.StatusDeprecated, time.Now().UTC()))
	exists, err = s.FindByContentHash(ctx, hash)
	require.NoError(t, err)
	assert.False(t, exists, "deprecated content must not block a re-propose")
}

// repairFixture inserts a deprecated memory with the given vote history.
func repairFixture(t *testing.T, s *SQLiteStore, id, content string, votes []*ValidationVote) {
	t.Helper()
	ctx := context.Background()
	rec := testMemory(id, "agent1", content, "general")
	require.NoError(t, s.InsertMemory(ctx, rec))
	require.NoError(t, s.UpdateStatus(ctx, id, memory.StatusDeprecated, time.Now().UTC()))
	for _, v := range votes {
		v.MemoryID = id
		if v.CreatedAt.IsZero() {
			v.CreatedAt = time.Now().UTC()
		}
		require.NoError(t, s.InsertVote(ctx, v))
	}
}

func TestRepairSelfDupRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const selfID = "se1f0000aabbcc"

	// Bug victim: exactly one vote — selfID rejecting as a self-duplicate.
	repairFixture(t, s, "victim", "wrongly deprecated by the self-match", []*ValidationVote{
		{ValidatorID: selfID, Decision: "reject", Rationale: "duplicate content (hash: deadbeef)"},
	})
	// Legit quality reject by the same validator: rationale differs → keep.
	repairFixture(t, s, "quality-reject", "short", []*ValidationVote{
		{ValidatorID: selfID, Decision: "reject", Rationale: "content too short (5 chars, minimum 20)"},
	})
	// Legacy 4-archetype history (3 accepts + the perpetual dedup reject) → keep.
	repairFixture(t, s, "legacy-era", "deprecated back in the archetype era", []*ValidationVote{
		{ValidatorID: "archetype-1", Decision: "accept", Rationale: "passes all checks"},
		{ValidatorID: "archetype-2", Decision: "accept", Rationale: "passes all checks"},
		{ValidatorID: "archetype-3", Decision: "accept", Rationale: "passes all checks"},
		{ValidatorID: "archetype-4", Decision: "reject", Rationale: "duplicate content (hash: cafe0123)"},
	})
	// Self-dup reject but ALSO challenged → keep (the challenge is decisive).
	repairFixture(t, s, "challenged", "deprecated by an explicit challenge", []*ValidationVote{
		{ValidatorID: selfID, Decision: "reject", Rationale: "duplicate content (hash: 0badf00d)"},
	})
	require.NoError(t, s.InsertChallenge(ctx, &ChallengeEntry{
		MemoryID: "challenged", ChallengerID: "agent2", Reason: "wrong", CreatedAt: time.Now().UTC(),
	}))

	var flipped []string
	repaired, err := s.RepairSelfDupRejected(ctx, selfID, func(id string) error {
		flipped = append(flipped, id)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)
	assert.Equal(t, []string{"victim"}, flipped)

	// The victim is proposed again, its bogus vote dropped.
	got, err := s.GetMemory(ctx, "victim")
	require.NoError(t, err)
	assert.Equal(t, memory.StatusProposed, got.Status)
	assert.Nil(t, got.DeprecatedAt)
	votes, err := s.GetVotes(ctx, "victim")
	require.NoError(t, err)
	assert.Empty(t, votes)

	// Everything else is untouched.
	for _, id := range []string{"quality-reject", "legacy-era", "challenged"} {
		kept, keptErr := s.GetMemory(ctx, id)
		require.NoError(t, keptErr)
		assert.Equal(t, memory.StatusDeprecated, kept.Status, "memory %s must stay deprecated", id)
	}

	// Re-running is a no-op: the fingerprint no longer matches the victim.
	repaired, err = s.RepairSelfDupRejected(ctx, selfID, func(string) error { return nil })
	require.NoError(t, err)
	assert.Zero(t, repaired)
}

// TestRepairSelfDupRejected_FlipChainFailure pins the re-entrancy contract: a
// chain-flip failure leaves the mirror row untouched, so the next startup's
// pass finds the same candidate again.
func TestRepairSelfDupRejected_FlipChainFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const selfID = "se1f0000aabbcc"

	repairFixture(t, s, "victim", "wrongly deprecated by the self-match", []*ValidationVote{
		{ValidatorID: selfID, Decision: "reject", Rationale: "duplicate content (hash: deadbeef)"},
	})

	repaired, err := s.RepairSelfDupRejected(ctx, selfID, func(string) error {
		return errors.New("badger unavailable")
	})
	require.Error(t, err)
	assert.Zero(t, repaired)

	got, err := s.GetMemory(ctx, "victim")
	require.NoError(t, err)
	assert.Equal(t, memory.StatusDeprecated, got.Status, "mirror must not flip when the chain flip failed")

	// Second pass (chain healthy again) repairs it.
	repaired, err = s.RepairSelfDupRejected(ctx, selfID, func(string) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)
	got, err = s.GetMemory(ctx, "victim")
	require.NoError(t, err)
	assert.Equal(t, memory.StatusProposed, got.Status)
}
