package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/store"
)

// GET /v1/agent/me must surface the on-chain PoE signals the audit found
// unreadable: the lifetime corroboration count, the authoritative
// verdict-correctness accuracy, and per-domain expertise — all read directly
// from BadgerDB (the consensus source), not the off-chain mirror.
func TestGetAgent_OnChainPoESignals(t *testing.T) {
	srv, _, _ := newTestServer(t, "")

	bs, err := store.NewBadgerStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.CloseBadger() })
	srv.badgerStore = bs

	req, agentID := signedRequest(t, http.MethodGet, "/v1/agent/me", nil)

	// Give the agent a domain so domain_expertise can populate.
	mock := newMockAgentStore()
	mock.domains[agentID] = []string{"pwn_heap"}
	srv.agentStore = mock

	// Seed on-chain PoE signals: two globally-matched verdicts (CorrCount=2,
	// EWMACount=2) and one matched verdict in pwn_heap.
	require.NoError(t, bs.UpdateVerdictStats(map[string]bool{agentID: true}))
	require.NoError(t, bs.UpdateVerdictStats(map[string]bool{agentID: true}))
	require.NoError(t, bs.UpdateDomainVerdictStats("pwn_heap", map[string]bool{agentID: true}))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp AgentProfileResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	assert.Equal(t, int64(2), resp.CorrCount, "two matched verdicts -> corr_count 2")
	assert.Greater(t, resp.Accuracy, 0.5, "two correct verdicts blend accuracy above the 0.5 cold-start")
	require.Contains(t, resp.DomainExpertise, "pwn_heap", "domain the agent voted in must appear")
	assert.Greater(t, resp.DomainExpertise["pwn_heap"], 0.5)
}

// With no on-chain history (empty BadgerStore) the new fields stay at their
// zero/cold-start values and the handler does not panic — the additive change
// is backward-compatible.
func TestGetAgent_OnChainPoESignals_ColdStart(t *testing.T) {
	srv, _, _ := newTestServer(t, "")

	bs, err := store.NewBadgerStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.CloseBadger() })
	srv.badgerStore = bs

	req, _ := signedRequest(t, http.MethodGet, "/v1/agent/me", nil)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp AgentProfileResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	assert.Equal(t, int64(0), resp.CorrCount)
	assert.Empty(t, resp.DomainExpertise, "no voting history -> no domain expertise emitted")
}
