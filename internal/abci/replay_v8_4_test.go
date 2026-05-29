package abci

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/memory"
	"github.com/l33tdawg/sage/internal/validator"
)

// v8.4 replay parity. The new on-chain keys (memdomain:<id>, vstats_domain:<v>:<D>)
// are written only post-fork; ComputeAppHash streams every key's raw bytes in
// lexical order, so (a) a pre-fork chain never sees these prefixes — its AppHash
// keyspace is byte-identical to v8.3.x (proven behaviorally by the e2e's pre-fork
// assertions, and structurally by the same mechanism as v8.2's R1/R2) — and
// (b) once the keys exist they must hash deterministically on every re-read, or
// replicas replaying the post-fork history would diverge. R3 pins (b): the v8.4
// keyspace is in the digest AND stable across reads.
func TestReplayV8_4_R3_PostForkKeysHashDeterministically(t *testing.T) {
	app := newAppWithLogger(t, zerolog.Nop())
	app.v8_2AppliedHeight = 1
	app.v8_3AppliedHeight = 1
	activateV84(app, 100) // v8.4 active for H>100

	vs := make([]agentKey, 4)
	for i := range vs {
		vs[i] = newAgentKey(t)
		require.NoError(t, app.validators.AddValidator(&validator.ValidatorInfo{ID: vs[i].id, Power: 1}))
	}
	proposer := vs[0]

	// Snapshot the AppHash BEFORE any v8.4 key is written.
	hBefore, err := ComputeAppHash(app.badgerStore)
	require.NoError(t, err)

	// A post-fork pwn_heap memory: submit writes memdomain:, the terminal verdict
	// writes vstats_domain:<v>:pwn_heap for all four validators.
	const memID = "mem-r3"
	const h = int64(200)
	require.True(t, app.postV8_4Fork(h))
	submitMemoryDomain(t, app, proposer, memID, "pwn_heap", h)
	castVote(t, app, vs[3], memID, false, h)
	castVote(t, app, vs[0], memID, true, h)
	castVote(t, app, vs[1], memID, true, h)
	castVote(t, app, vs[2], memID, true, h)
	require.Equal(t, string(memory.StatusCommitted), statusOf(t, app, memID))

	// The v8.4 keys now exist on-chain.
	d, err := app.badgerStore.GetMemoryDomain(memID)
	require.NoError(t, err)
	require.Equal(t, "pwn_heap", d)
	ec, _ := vdomStatsOf(t, app, vs[0].id, "pwn_heap")
	require.Equal(t, uint64(1), ec)

	// (a) The new keys are part of the digest — AppHash moved off the pre-write snapshot.
	hAfter, err := ComputeAppHash(app.badgerStore)
	require.NoError(t, err)
	assert.NotEqual(t, hBefore, hAfter, "memdomain:/vstats_domain: keys must contribute to the AppHash")

	// (b) Replay-safety: ComputeAppHash over the same post-fork keyspace is
	// byte-identical on every read — no map-iteration nondeterminism leaks in.
	hReplay, err := ComputeAppHash(app.badgerStore)
	require.NoError(t, err)
	assert.Equal(t, hAfter, hReplay, "ComputeAppHash must be deterministic over the v8.4 keyspace")
}
