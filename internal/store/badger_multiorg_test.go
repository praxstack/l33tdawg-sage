package store

import (
	"encoding/binary"
	"sort"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLegacyMembership writes only the org_member forward entry — no
// agent_orgs reverse index — so backfill tests can mimic data produced by
// pre-v6.6.8 binaries that didn't maintain the multi-org reverse index.
func writeLegacyMembership(bs *BadgerStore, orgID, agentID string, clearance uint8, role string, height int64) error {
	return bs.db.Update(func(txn *badger.Txn) error {
		val := make([]byte, 1+4+len(role)+8)
		val[0] = clearance
		encodeString(val, 1, role)
		binary.BigEndian.PutUint64(val[1+4+len(role):], uint64(height)) // #nosec G115 -- block height is non-negative
		return txn.Set(orgMemberKey(orgID, agentID), val)
	})
}

// v6.6.8 regression: an agent that joins a second org used to lose visibility
// into memories from their original org because agent_org:<agent> was a
// single-slot reverse lookup that AddOrgMember silently overwrote, and
// HasAccessMultiOrg only consulted that one slot. These tests pin the
// one-to-many agent_orgs:<agent>:<org> index in place so AddOrgMember stays
// additive and HasAccessMultiOrg keeps iterating.

func newTestBadger(t *testing.T) *BadgerStore {
	t.Helper()
	bs, err := NewBadgerStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.CloseBadger() })
	return bs
}

func TestMultiOrg_AddOrgMember_PreservesPriorMemberships(t *testing.T) {
	bs := newTestBadger(t)

	const orgA = "0aaa11111111111111111111111111aa"
	const orgB = "0bbb22222222222222222222222222bb"
	const agentA = "agentA000000000000000000000000000000000000000000000000000000aaaa"
	const agentX = "agentX000000000000000000000000000000000000000000000000000000xxxx"

	require.NoError(t, bs.RegisterOrg(orgA, "Org A", "", agentA, 1))
	require.NoError(t, bs.AddOrgMember(orgA, agentA, 4, "admin", 1))
	require.NoError(t, bs.AddOrgMember(orgA, agentX, 4, "member", 2))

	// Agent X is currently a member of org A only.
	orgs, err := bs.ListAgentOrgs(agentX)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{orgA}, orgs)

	// Agent X joins org B. The legacy single-slot would overwrite — the
	// multi-org index must NOT.
	require.NoError(t, bs.RegisterOrg(orgB, "Org B", "", agentA, 3))
	require.NoError(t, bs.AddOrgMember(orgB, agentX, 4, "member", 4))

	orgs, err = bs.ListAgentOrgs(agentX)
	require.NoError(t, err)
	sort.Strings(orgs)
	assert.Equal(t, []string{orgA, orgB}, orgs, "agent must remain a member of both orgs")

	inA, err := bs.IsAgentInOrg(agentX, orgA)
	require.NoError(t, err)
	assert.True(t, inA, "agent must still be in org A after joining org B")

	inB, err := bs.IsAgentInOrg(agentX, orgB)
	require.NoError(t, err)
	assert.True(t, inB, "agent must be in org B")
}

func TestMultiOrg_HasAccess_DoesNotDependOnPrimarySlot(t *testing.T) {
	bs := newTestBadger(t)

	// Two orgs, two domains — one per org.
	const orgA = "0aaa11111111111111111111111111aa"
	const orgB = "0bbb22222222222222222222222222bb"
	const adminA = "adminA00000000000000000000000000000000000000000000000000000aaaa"
	const adminB = "adminB00000000000000000000000000000000000000000000000000000bbbb"
	const pipelineAgent = "pipeline000000000000000000000000000000000000000000000000000pipe"

	require.NoError(t, bs.RegisterOrg(orgA, "Org A", "", adminA, 1))
	require.NoError(t, bs.AddOrgMember(orgA, adminA, 4, "admin", 1))
	require.NoError(t, bs.RegisterDomain("orga.secret", adminA, "", 1))

	require.NoError(t, bs.RegisterOrg(orgB, "Org B", "", adminB, 2))
	require.NoError(t, bs.AddOrgMember(orgB, adminB, 4, "admin", 2))
	require.NoError(t, bs.RegisterDomain("orgb.secret", adminB, "", 2))

	// Pipeline agent joins org A first, then org B. Under the legacy
	// single-slot lookup the second AddOrgMember would clobber the agent's
	// "primary org" to B and HasAccessMultiOrg would deny access to org A's
	// domain even though the membership entry survived.
	require.NoError(t, bs.AddOrgMember(orgA, pipelineAgent, 4, "member", 3))
	require.NoError(t, bs.AddOrgMember(orgB, pipelineAgent, 4, "member", 4))

	now := time.Unix(10000, 0)

	okA, err := bs.HasAccessMultiOrg("orga.secret", pipelineAgent, 0, now)
	require.NoError(t, err)
	assert.True(t, okA, "multi-org member must keep visibility into org A's domain after joining org B")

	okB, err := bs.HasAccessMultiOrg("orgb.secret", pipelineAgent, 0, now)
	require.NoError(t, err)
	assert.True(t, okB, "multi-org member must also have visibility into org B's domain")

	// Sanity check: a non-member is denied, regardless of slot state.
	const stranger = "stranger00000000000000000000000000000000000000000000000000strg"
	okStranger, err := bs.HasAccessMultiOrg("orga.secret", stranger, 0, now)
	require.NoError(t, err)
	assert.False(t, okStranger, "non-member must be denied")
}

func TestMultiOrg_RemoveOrgMember_KeepsOtherMemberships(t *testing.T) {
	bs := newTestBadger(t)

	const orgA = "0aaa11111111111111111111111111aa"
	const orgB = "0bbb22222222222222222222222222bb"
	const admin = "admin0000000000000000000000000000000000000000000000000000000adm"
	const agentX = "agentX000000000000000000000000000000000000000000000000000000xxxx"

	require.NoError(t, bs.RegisterOrg(orgA, "Org A", "", admin, 1))
	require.NoError(t, bs.RegisterOrg(orgB, "Org B", "", admin, 2))
	require.NoError(t, bs.AddOrgMember(orgA, agentX, 3, "member", 3))
	require.NoError(t, bs.AddOrgMember(orgB, agentX, 3, "member", 4))

	require.NoError(t, bs.RemoveOrgMember(orgB, agentX))

	orgs, err := bs.ListAgentOrgs(agentX)
	require.NoError(t, err)
	assert.Equal(t, []string{orgA}, orgs, "removing org B membership must not strip org A")

	inA, err := bs.IsAgentInOrg(agentX, orgA)
	require.NoError(t, err)
	assert.True(t, inA)
	inB, err := bs.IsAgentInOrg(agentX, orgB)
	require.NoError(t, err)
	assert.False(t, inB)

	// Legacy single-slot must still resolve to a valid remaining org so callers
	// (governance handlers) that auto-pick a "primary" don't see a phantom org.
	primary, err := bs.GetAgentOrg(agentX)
	require.NoError(t, err)
	assert.Equal(t, orgA, primary)

	// Removing the last membership clears the legacy slot too.
	require.NoError(t, bs.RemoveOrgMember(orgA, agentX))
	orgs, err = bs.ListAgentOrgs(agentX)
	require.NoError(t, err)
	assert.Empty(t, orgs)
	if _, err := bs.GetAgentOrg(agentX); err == nil {
		t.Fatalf("expected GetAgentOrg to fail for non-member agent")
	}
}

func TestMultiOrg_EnsureAgentOrgsIndex_BackfillsLegacyData(t *testing.T) {
	bs := newTestBadger(t)

	const orgA = "0aaa11111111111111111111111111aa"
	const orgB = "0bbb22222222222222222222222222bb"
	const admin = "admin0000000000000000000000000000000000000000000000000000000adm"
	const legacyAgent = "legacy0000000000000000000000000000000000000000000000000000legacy"

	// Set up the forward index without writing the new reverse index, mirroring
	// the schema produced by pre-v6.6.8 binaries: only org_member:* exists,
	// agent_orgs:* is missing.
	require.NoError(t, bs.RegisterOrg(orgA, "Org A", "", admin, 1))
	require.NoError(t, bs.RegisterOrg(orgB, "Org B", "", admin, 2))
	require.NoError(t, writeLegacyMembership(bs, orgA, legacyAgent, 4, "member", 3))
	require.NoError(t, writeLegacyMembership(bs, orgB, legacyAgent, 4, "member", 4))

	// Before backfill the multi-org reverse index is empty.
	orgs, err := bs.ListAgentOrgs(legacyAgent)
	require.NoError(t, err)
	assert.Empty(t, orgs, "precondition: legacy membership has no agent_orgs entries")

	require.NoError(t, bs.EnsureAgentOrgsIndex())

	orgs, err = bs.ListAgentOrgs(legacyAgent)
	require.NoError(t, err)
	sort.Strings(orgs)
	assert.Equal(t, []string{orgA, orgB}, orgs, "backfill must rebuild the reverse index from org_member entries")

	// Idempotent — second call must not duplicate or error.
	require.NoError(t, bs.EnsureAgentOrgsIndex())
	orgs, err = bs.ListAgentOrgs(legacyAgent)
	require.NoError(t, err)
	sort.Strings(orgs)
	assert.Equal(t, []string{orgA, orgB}, orgs)
}
