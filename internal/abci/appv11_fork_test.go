package abci

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// app-v11 fork (v10.0): consensus-path SQL-admin-bootstrap disable (#36) +
// deterministic chain-admin established at the activation block (#35).
//   - bootstrapAdminFromSQL is suppressed post-fork (postAppV11Rules), removing
//     the per-node-SQL → BadgerDB write that diverged the AppHash on
//     multi-validator chains.
//   - the FinalizeBlock activation arm calls materializeAppV11Admin, which — only
//     when no on-chain admin exists — registers the lexicographically-smallest
//     committed validator as admin (a pure function of committed state).
// Gated so every existing chain (appV11AppliedHeight==0) replays byte-identically;
// app-v11 also SUBSUMES app-v8/v9/v10 rules via the extended postAppV8Rules/
// postAppV9Rules and the new postAppV10Rules helper.
// ---------------------------------------------------------------------------

// TestAppV11_SuppressesSQLAdminBootstrap is the #36 guard: post-fork the per-node
// SQL→BadgerDB admin bootstrap must NOT fire (no AppHash-affecting write), while
// pre-fork it still self-heals exactly as before (replay parity).
func TestAppV11_SuppressesSQLAdminBootstrap(t *testing.T) {
	t.Run("post-fork: suppressed, no BadgerDB write", func(t *testing.T) {
		app := setupTestApp(t)
		admin := newAgentKey(t)
		seedSQLAgent(t, app, admin.id, "admin-agent", "admin", 4)
		app.appV11AppliedHeight = 1 // fork active

		got, ok := app.bootstrapAdminFromSQL(admin.id, 5, time.Now())
		assert.False(t, ok, "post-app-v11 the SQL bootstrap must be disabled on the consensus path")
		assert.Nil(t, got)
		_, err := app.badgerStore.GetRegisteredAgent(admin.id)
		assert.Error(t, err, "suppressed bootstrap must not write an agent: record (AppHash-divergence hazard)")
	})

	t.Run("pre-fork: still self-heals (replay parity)", func(t *testing.T) {
		app := setupTestApp(t)
		admin := newAgentKey(t)
		seedSQLAgent(t, app, admin.id, "admin-agent", "admin", 4)
		// appV11AppliedHeight == 0 ⇒ gate dormant ⇒ legacy path runs.

		got, ok := app.bootstrapAdminFromSQL(admin.id, 5, time.Now())
		require.True(t, ok, "pre-fork the legacy SQL bootstrap must still materialize the admin")
		require.NotNil(t, got)
		assert.Equal(t, "admin", got.Role)
		onChain, err := app.badgerStore.GetRegisteredAgent(admin.id)
		require.NoError(t, err)
		assert.Equal(t, "admin", onChain.Role)
	})
}

// TestMaterializeAppV11Admin covers the #35 activation-block admin establishment:
// a NO-OP when an admin already exists (the normal climb), and a deterministic
// smallest-validator mint when none does (the degenerate admin-less case).
func TestMaterializeAppV11Admin(t *testing.T) {
	t.Run("no-op when an admin already exists", func(t *testing.T) {
		app := setupTestApp(t)
		admin := newAgentKey(t)
		require.NoError(t, app.badgerStore.RegisterAgent(admin.id, "ops", "admin", "", "", "", 1))
		valA, valB := newAgentKey(t), newAgentKey(t)
		require.NoError(t, app.badgerStore.SaveValidators(map[string]int64{valA.id: 10, valB.id: 10}))

		app.materializeAppV11Admin(5)

		// Neither validator was minted as admin; the existing admin is untouched.
		for _, v := range []agentKey{valA, valB} {
			if a, err := app.badgerStore.GetRegisteredAgent(v.id); err == nil {
				assert.NotEqual(t, "admin", a.Role, "must not mint a validator-admin when an admin exists")
			}
		}
		got, err := app.badgerStore.GetRegisteredAgent(admin.id)
		require.NoError(t, err)
		assert.Equal(t, "admin", got.Role)
	})

	t.Run("mints the smallest validator when no admin exists", func(t *testing.T) {
		app := setupTestApp(t)
		valA, valB := newAgentKey(t), newAgentKey(t)
		require.NoError(t, app.badgerStore.SaveValidators(map[string]int64{valA.id: 10, valB.id: 10}))
		smaller := valA.id
		if valB.id < smaller {
			smaller = valB.id
		}

		app.materializeAppV11Admin(7)

		got, err := app.badgerStore.GetRegisteredAgent(smaller)
		require.NoError(t, err, "the smallest validator must be materialized as admin")
		assert.Equal(t, "admin", got.Role)
	})

	t.Run("elevates an already-registered validator, preserving its identity", func(t *testing.T) {
		app := setupTestApp(t)
		valA, valB := newAgentKey(t), newAgentKey(t)
		require.NoError(t, app.badgerStore.SaveValidators(map[string]int64{valA.id: 10, valB.id: 10}))
		smaller := valA.id
		if valB.id < smaller {
			smaller = valB.id
		}
		// The chosen validator is already a registered member with metadata.
		require.NoError(t, app.badgerStore.RegisterAgent(smaller, "node-op", "member", "my bio", "claude", "/ip4/1.2.3.4/tcp/26656", 1))

		app.materializeAppV11Admin(7)

		got, err := app.badgerStore.GetRegisteredAgent(smaller)
		require.NoError(t, err)
		assert.Equal(t, "admin", got.Role, "elevated to admin")
		assert.Equal(t, "node-op", got.Name, "existing identity preserved, not blind-overwritten")
		assert.Equal(t, "my bio", got.BootBio)
	})

	t.Run("no validators ⇒ no panic, no admin", func(t *testing.T) {
		app := setupTestApp(t)
		assert.NotPanics(t, func() { app.materializeAppV11Admin(3) })
	})
}

// TestAppV11_ActivationMaterialize_Deterministic is the multi-validator
// AppHash-determinism gate for #35: two nodes with the IDENTICAL committed
// validator set and no admin must materialize the SAME admin and end at a
// BYTE-IDENTICAL AppHash — the real consensus gate.
func TestAppV11_ActivationMaterialize_Deterministic(t *testing.T) {
	valA, valB := newAgentKey(t), newAgentKey(t)
	vals := map[string]int64{valA.id: 10, valB.id: 10}

	nodeA := setupTestApp(t)
	nodeB := setupTestApp(t)
	require.NoError(t, nodeA.badgerStore.SaveValidators(vals))
	require.NoError(t, nodeB.badgerStore.SaveValidators(vals))

	nodeA.materializeAppV11Admin(9)
	nodeB.materializeAppV11Admin(9)

	hashA, err := ComputeAppHash(nodeA.badgerStore)
	require.NoError(t, err)
	hashB, err := ComputeAppHash(nodeB.badgerStore)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB, "the activation-block admin materialization must fold into an identical AppHash on every node (#35)")

	smaller := valA.id
	if valB.id < smaller {
		smaller = valB.id
	}
	got, err := nodeA.badgerStore.GetRegisteredAgent(smaller)
	require.NoError(t, err)
	assert.Equal(t, "admin", got.Role)
}

// TestAppV11_SQLSuppression_DeterministicAcrossDivergentSQL is the #36 gate: two
// nodes with IDENTICAL committed Badger state but DIVERGENT per-node SQL admin
// rows — the exact condition that diverged the AppHash before this fork — must
// keep an identical AppHash once app-v11 suppresses the SQL bootstrap.
func TestAppV11_SQLSuppression_DeterministicAcrossDivergentSQL(t *testing.T) {
	nodeA := setupTestApp(t)
	nodeB := setupTestApp(t)

	// Identical committed base state on both nodes (so AppHash equality is
	// meaningful, not just two empty stores).
	base := newAgentKey(t)
	require.NoError(t, nodeA.badgerStore.RegisterAgent(base.id, "base", "member", "", "", "", 0))
	require.NoError(t, nodeB.badgerStore.RegisterAgent(base.id, "base", "member", "", "", "", 0))

	// Divergent per-node SQL: each node's local SQL admins a DIFFERENT key (exactly
	// how seedNetworkAgents seeds each node's own validator slot).
	adminA, adminB := newAgentKey(t), newAgentKey(t)
	seedSQLAgent(t, nodeA, adminA.id, "admin-a", "admin", 4)
	seedSQLAgent(t, nodeB, adminB.id, "admin-b", "admin", 4)

	nodeA.appV11AppliedHeight = 1
	nodeB.appV11AppliedHeight = 1

	// Each node runs its own admin op signed by its own local admin key. Post-fork
	// the bootstrap is suppressed, so neither writes a Badger record.
	_, okA := nodeA.bootstrapAdminFromSQL(adminA.id, 5, time.Now())
	_, okB := nodeB.bootstrapAdminFromSQL(adminB.id, 5, time.Now())
	assert.False(t, okA)
	assert.False(t, okB)

	hashA, err := ComputeAppHash(nodeA.badgerStore)
	require.NoError(t, err)
	hashB, err := ComputeAppHash(nodeB.badgerStore)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB, "post-app-v11 divergent per-node SQL must NOT diverge the AppHash (#36)")
}

// TestAppV11_ForkGateDefaultsAndSubsumption locks the gate's dormant default and
// the subsumption discipline — a chain that skip-activates app-v11 without app-v10
// must still enforce app-v8/v9/v10's rules, and must report version 11.
func TestAppV11_ForkGateDefaultsAndSubsumption(t *testing.T) {
	app := setupTestApp(t)
	// Dormant by default ⇒ every existing chain replays the pre-fork branch.
	assert.False(t, app.postAppV11Fork(100), "gate dormant by default")
	assert.False(t, app.postAppV11Rules(100), "rules dormant by default")
	assert.Equal(t, uint64(1), app.currentAppVersion(), "no gate ⇒ version 1")

	// Skip-ahead: only app-v11 active (app-v8/v9/v10 dormant).
	app.appV11AppliedHeight = 5
	assert.False(t, app.postAppV11Fork(5), "strict greater-than: not active AT the activation height")
	assert.True(t, app.postAppV11Fork(6), "active at H+1")
	assert.True(t, app.postAppV11Rules(6))
	// Subsumption: the lower forks' rules must be in force on this app-v11 chain
	// even though their own gates are 0.
	assert.True(t, app.postAppV10Rules(6), "app-v11 subsumes app-v10's rules")
	assert.True(t, app.postAppV9Rules(6), "app-v11 subsumes app-v9's rules")
	assert.True(t, app.postAppV8Rules(6), "app-v11 subsumes app-v8's rules")
	assert.Equal(t, uint64(11), app.currentAppVersion(), "app-v11 active (alone) reports 11")
}
