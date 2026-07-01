package abci

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/store"
)

// app-v15 replay parity (v11). app-v15 is the EMPTY next-free scaffolding gate:
// it wires NO new behavior — no new tx types, no new AppHash rule — so it exists
// only to reserve the version slot and to be a live-fire exercise of the full
// propose→vote→activate→persist→boot-restore→replay path on a no-op, de-risking
// the next (behavioral) fork. Unlike app-v14 (a deactivation embedded in
// postAppV7Fork) it IS additive/subsuming: postAppV15Fork is OR'd into
// postAppV8Rules..postAppV11Rules so a skip-ahead-to-15 chain still enforces the
// lower additive rules.
//
// These tests pin:
//   ForkGateDefaultsAndSubsumption — dormant by default, strict-> boundary, and
//      once live it subsumes the app-v8..v11 additive rules.
//   SkipAheadSubsumption — the CORE invariant: a 6→15 skip with only
//      appV15AppliedHeight set keeps app-v8..v11 rules TRUE while the mutually-
//      exclusive AppHash-replacement helpers (v12/v13) stay FALSE. This is the
//      inversion of app-v14 D6 — the whole reason the OR-ins exist.
//   BootRefreshThroughConstructor — the activation height restores through the
//      REAL constructor path, and reconcilePoEForkMonotonicity leaves it alone.
//   CrashReplayReEmitsVersionBump — a replayed app-v15 activation block re-emits
//      ConsensusParamUpdates(version.app=15) from the audit trail.

// TestAppV15_ForkGateDefaultsAndSubsumption pins the dormant default, the strict-'>'
// activation boundary, and that a live gate subsumes the lower additive rules.
func TestAppV15_ForkGateDefaultsAndSubsumption(t *testing.T) {
	app := setupTestApp(t)
	assert.False(t, app.postAppV15Fork(100), "gate dormant by default (appV15AppliedHeight == 0)")
	assert.Equal(t, uint64(1), app.currentAppVersion(), "no gate ⇒ genesis version 1")

	app.appV15AppliedHeight = 5
	assert.False(t, app.postAppV15Fork(5), "activation block itself is not past-fork (strict >)")
	assert.True(t, app.postAppV15Fork(6), "live at H_act+1")

	// Subsumption: with only app-v15 set, the app-v8..v11 additive rules are live.
	assert.True(t, app.postAppV8Rules(6), "app-v15 subsumes app-v8 rules")
	assert.True(t, app.postAppV11Rules(6), "app-v15 subsumes app-v11 rules")
	assert.Equal(t, uint64(15), app.currentAppVersion())
}

// TestReplayAppV15_SkipAheadSubsumption is the core property: a hand-crafted
// 6→15 skip-ahead (only appV15AppliedHeight set, every lower independent gate
// still 0) must NOT leave the additive rules false while currentAppVersion
// reports 15 — reconcilePoEForkMonotonicity only backfills the PoE ladder
// (app-v2..v6), so the four subsumption OR-ins are the ONLY safety net. It is the
// inversion of app-v14 D6, which asserts the additive rules FALSE — do not copy
// that here. Two independent replicas must agree at every probed height.
func TestReplayAppV15_SkipAheadSubsumption(t *testing.T) {
	mk := func() *SageApp {
		a := setupTestApp(t)
		a.appV15AppliedHeight = 100 // only the top gate; app-v8..v13 fields stay 0
		return a
	}
	app, replica := mk(), mk()

	// Additive rules SUBSUMED via the OR-in (skip-ahead safety net):
	assert.True(t, app.postAppV8Rules(101), "v15 subsumes app-v8 rules")
	assert.True(t, app.postAppV9Rules(101), "v15 subsumes app-v9 rules")
	assert.True(t, app.postAppV10Rules(101), "v15 subsumes app-v10 rules")
	assert.True(t, app.postAppV11Rules(101), "v15 subsumes app-v11 rules")

	// AppHash-replacement helpers are NOT subsumed (mutually exclusive; app-v15
	// adds no hash rule). ORing v15 into these would misselect the FinalizeBlock
	// hash rule and diverge the AppHash.
	assert.False(t, app.postAppV12Rules(101), "v15 does NOT imply the v12 AppHash rule")
	assert.False(t, app.postAppV13Rules(101), "v15 does NOT imply the v13 AppHash rule")

	assert.Equal(t, uint64(15), app.currentAppVersion())
	assert.Equal(t, int64(0), app.v8AppliedHeight, "skip-ahead: PoE ladder untouched")

	// Determinism: the rule set is a pure function of the committed activation
	// height, so two identically-configured replicas agree everywhere.
	for _, h := range []int64{1, 100, 101, 1000} {
		assert.Equalf(t, app.postAppV8Rules(h), replica.postAppV8Rules(h), "replicas agree on v8 rules at %d", h)
		assert.Equalf(t, app.postAppV15Fork(h), replica.postAppV15Fork(h), "replicas agree on v15 gate at %d", h)
	}
}

// TestReplayAppV15_BootRefreshThroughConstructor asserts a node restarting on a
// post-app-v15 chain restores the activation height through the REAL constructor
// path (both NewSageApp and NewSageAppWithStores call refreshAppV15Fork), and
// reconcilePoEForkMonotonicity leaves the independent gate alone.
func TestReplayAppV15_BootRefreshThroughConstructor(t *testing.T) {
	bs := setupTestBadger(t)
	dbPath := filepath.Join(t.TempDir(), "appv15-boot-refresh.db")
	sqlite, err := store.NewSQLiteStore(context.Background(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { sqlite.Close() })

	require.NoError(t, bs.MarkUpgradeApplied(appV15UpgradeName, 15, 4200))

	app, err := NewSageAppWithStores(bs, sqlite, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, int64(4200), app.appV15AppliedHeight, "constructor must restore the app-v15 activation height")
	assert.Equal(t, uint64(15), app.currentAppVersion())
	assert.False(t, app.postAppV15Fork(4200), "gate dormant at the activation block (strict >)")
	assert.True(t, app.postAppV15Fork(4201), "gate live at H_act+1")
	assert.Equal(t, int64(0), app.v8AppliedHeight, "independent gate: PoE ladder untouched by reconcile")
}

// TestReplayAppV15_CrashReplayReEmitsVersionBump simulates the crash-before-Commit
// replay of an app-v15 activation block: the plan is already deleted
// (MarkUpgradeApplied is durable), so the replayed H_act must re-emit the
// version.app=15 bump from the audit trail — matching every non-crashed replica.
// This exercises the FinalizeBlock activation arm added for app-v15.
func TestReplayAppV15_CrashReplayReEmitsVersionBump(t *testing.T) {
	app := setupTestApp(t)
	proposer := deterministicAgentKey(t)
	// Stage the app-v15 plan through the pre-app-v8 self-activating path.
	ptx := makeUpgradeProposeTx(t, proposer, appV15UpgradeName, 15, "feed", 0)
	require.Equal(t, uint32(0), app.processUpgradePropose(ptx, 11, time.Unix(0, 0)).Code)
	plan, err := app.badgerStore.GetUpgradePlan()
	require.NoError(t, err)
	actH := plan.ActivationHeight

	respOrig, err := app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{Height: actH, Time: time.Now()})
	require.NoError(t, err)
	require.NotNil(t, respOrig.ConsensusParamUpdates)
	require.Equal(t, uint64(15), respOrig.ConsensusParamUpdates.Version.App)
	assert.Equal(t, actH, app.appV15AppliedHeight, "activation sets the app-v15 height in memory")

	// Crash before Commit; CometBFT replays H_act. No plan exists anymore.
	respReplay, err := app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{Height: actH, Time: time.Now()})
	require.NoError(t, err)
	require.NotNil(t, respReplay.ConsensusParamUpdates, "replayed activation must re-emit the version bump from the audit trail")
	assert.Equal(t, uint64(15), respReplay.ConsensusParamUpdates.Version.App)

	// A non-activation height must NOT re-emit.
	respOther, err := app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{Height: actH + 1, Time: time.Now()})
	require.NoError(t, err)
	assert.Nil(t, respOther.ConsensusParamUpdates)
}
