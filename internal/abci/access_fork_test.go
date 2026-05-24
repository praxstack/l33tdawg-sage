package abci

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v8.0 access-control fork: pin the boundary semantics that gate the new
// ancestor-walk behaviour in HasAccessMultiOrg. Pre-fork must be byte-identical
// to v7.1.1 (exact-match grant + exact-match owner), post-fork must walk the
// dotted-domain path so a grant or ownership on a parent covers descendants.

// 64-hex-char fake IDs — production shape, no entropy required.
const (
	forkOwnerID  = "ffffowneerrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrffffff"
	forkAgentID  = "ffffaaagent00000000000000000000000000000000000000000000000ffffff"
	forkGranter  = "ffffgrantor00000000000000000000000000000000000000000000000ffffff"
	forkOtherID  = "ffffotherrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrffffff"
	forkOrgA     = "0aaa11111111111111111111111111aa"
	forkOrgB     = "0bbb22222222222222222222222222bb"
	forkAdminA   = "ffffadminAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAffffff"
	forkAdminB   = "ffffadminBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBffffff"
	forkAgentA   = "ffffagentAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAffffff"
	forkAgentInA = "ffffagentInAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaffffffff"
)

// F1: pre-fork (v8AppliedHeight == 0). A grant on the parent must NOT cover
// the child — the access check goes through HasAccess exact-match.
func TestAccessFork_PreFork_ParentGrantDoesNotCoverChild(t *testing.T) {
	app := setupTestApp(t)
	require.Equal(t, int64(0), app.v8AppliedHeight, "precondition: fork inactive")

	// Register the parent domain owned by some other agent so the access path
	// has to consult the grant entry rather than auto-registering.
	require.NoError(t, app.badgerStore.RegisterDomain("pipeline.failures", forkOwnerID, "", 1))
	require.NoError(t, app.badgerStore.RegisterDomain("pipeline.failures.pwn_buffer_overflow", forkOwnerID, "", 1))
	// Grant on the parent only.
	require.NoError(t, app.badgerStore.SetAccessGrant("pipeline.failures", forkAgentID, 1, 0, forkGranter))

	ok, err := app.badgerStore.HasAccessMultiOrg(
		"pipeline.failures.pwn_buffer_overflow", forkAgentID, 0,
		time.Unix(2000, 0), app.postV8Fork(50),
	)
	require.NoError(t, err)
	assert.False(t, ok, "pre-fork must not cascade parent grant to child")
}

// F2: post-fork (v8AppliedHeight=100, request height=200). Same scenario:
// grant on the parent now covers the child via the ancestor walk.
func TestAccessFork_PostFork_ParentGrantCoversChild(t *testing.T) {
	app := setupTestApp(t)
	app.v8AppliedHeight = 100
	require.True(t, app.postV8Fork(200), "precondition: fork active at H=200")

	require.NoError(t, app.badgerStore.RegisterDomain("pipeline.failures", forkOwnerID, "", 1))
	require.NoError(t, app.badgerStore.RegisterDomain("pipeline.failures.pwn_buffer_overflow", forkOwnerID, "", 1))
	require.NoError(t, app.badgerStore.SetAccessGrant("pipeline.failures", forkAgentID, 1, 0, forkGranter))

	ok, err := app.badgerStore.HasAccessMultiOrg(
		"pipeline.failures.pwn_buffer_overflow", forkAgentID, 0,
		time.Unix(2000, 0), app.postV8Fork(200),
	)
	require.NoError(t, err)
	assert.True(t, ok, "post-fork must cascade parent grant to child")
}

// F3: same-org clearance via ResolveOwningAncestor. The leaf
// "corp.eng.builds" is NOT registered, but the ancestor "corp.eng" is
// owned by an agent whose org also contains the querying agent at sufficient
// clearance — that ancestor walk + same-org check should grant access.
func TestAccessFork_PostFork_SameOrgClearance_AncestorOwnership(t *testing.T) {
	app := setupTestApp(t)
	app.v8AppliedHeight = 100

	bs := app.badgerStore

	// Org A with an admin + a member agent.
	require.NoError(t, bs.RegisterOrg(forkOrgA, "Org A", "desc", forkAdminA, 1))
	require.NoError(t, bs.AddOrgMember(forkOrgA, forkAdminA, 4, "admin", 1))
	require.NoError(t, bs.AddOrgMember(forkOrgA, forkAgentInA, 2, "member", 1))

	// Only the ancestor is registered, owned by an agent in org A.
	require.NoError(t, bs.RegisterDomain("corp.eng", forkAdminA, "", 1))

	// Querying agent is in org A at clearance 2; the memory classification
	// we'll request is 0, so the clearance bar is easily satisfied.
	ok, err := bs.HasAccessMultiOrg(
		"corp.eng.builds", forkAgentInA, 0,
		time.Unix(2000, 0), app.postV8Fork(200),
	)
	require.NoError(t, err)
	assert.True(t, ok, "post-fork: same-org clearance must apply when ancestor is owned by a same-org agent")

	// Pre-fork sanity: the same call with postFork=false fails because the
	// leaf is unregistered and the old code path bails out at GetDomainOwner.
	okPre, err := bs.HasAccessMultiOrg(
		"corp.eng.builds", forkAgentInA, 0,
		time.Unix(2000, 0), false,
	)
	require.NoError(t, err)
	assert.False(t, okPre, "pre-fork must not resolve ancestor ownership")
}

// F4: federation. Agent in org A, the queried domain's ancestor is owned
// by an agent in org B, with an active A↔B federation. Post-fork should
// grant via the inherited ancestor ownership.
func TestAccessFork_PostFork_Federation_AncestorOwnership(t *testing.T) {
	app := setupTestApp(t)
	app.v8AppliedHeight = 100

	bs := app.badgerStore

	// Two orgs, two admins, plus a member of A.
	require.NoError(t, bs.RegisterOrg(forkOrgA, "Org A", "", forkAdminA, 1))
	require.NoError(t, bs.AddOrgMember(forkOrgA, forkAdminA, 4, "admin", 1))
	require.NoError(t, bs.AddOrgMember(forkOrgA, forkAgentInA, 3, "member", 1))

	require.NoError(t, bs.RegisterOrg(forkOrgB, "Org B", "", forkAdminB, 1))
	require.NoError(t, bs.AddOrgMember(forkOrgB, forkAdminB, 4, "admin", 1))

	// Domain ancestor "partners.corp" owned by an org-B admin. Leaf is
	// "partners.corp.docs" — not directly registered.
	require.NoError(t, bs.RegisterDomain("partners.corp", forkAdminB, "", 1))

	// Active A↔B federation, no expiry, max clearance 4, no dept restriction.
	const fedID = "fed-A-B-1234567890123456789012345678901234"
	require.NoError(t, bs.SetFederation(fedID, forkOrgA, forkOrgB, []string{}, 4, 0, false, "active"))

	ok, err := bs.HasAccessMultiOrg(
		"partners.corp.docs", forkAgentInA, 0,
		time.Unix(2000, 0), app.postV8Fork(200),
	)
	require.NoError(t, err)
	assert.True(t, ok, "post-fork: federation must inherit ancestor ownership for descendant lookups")
}

// F5: boundary semantics — height == activation block is still pre-fork
// (the H+1 rule from CometBFT's ConsensusParamUpdates), height == activation+1
// flips to post-fork. Same scenario, exercised at both heights, must produce
// opposite results.
func TestAccessFork_BoundaryHPlusOne(t *testing.T) {
	app := setupTestApp(t)
	app.v8AppliedHeight = 100
	bs := app.badgerStore

	require.NoError(t, bs.RegisterDomain("pipeline.failures", forkOwnerID, "", 1))
	require.NoError(t, bs.RegisterDomain("pipeline.failures.pwn_buffer_overflow", forkOwnerID, "", 1))
	require.NoError(t, bs.SetAccessGrant("pipeline.failures", forkAgentID, 1, 0, forkGranter))

	// At H=100 (activation block): predicate says pre-fork, so parent grant
	// must NOT cover the child.
	okAt, err := bs.HasAccessMultiOrg(
		"pipeline.failures.pwn_buffer_overflow", forkAgentID, 0,
		time.Unix(2000, 0), app.postV8Fork(100),
	)
	require.NoError(t, err)
	assert.False(t, okAt, "at activation block: pre-fork — exact-match required")

	// At H=101 (first post-activation block): post-fork, ancestor walk active.
	okAfter, err := bs.HasAccessMultiOrg(
		"pipeline.failures.pwn_buffer_overflow", forkAgentID, 0,
		time.Unix(2000, 0), app.postV8Fork(101),
	)
	require.NoError(t, err)
	assert.True(t, okAfter, "H+1: post-fork — ancestor grant cascades")
}
