package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Per-domain read ACL on GET /v1/memory/list
//
// Regression for the multi-node (v10.1.0) report: list_memories did
// NOT run the checkDomainAccess gate that /query and /hybrid enforce, so an
// agent with no read grant on a domain could enumerate that domain's record
// CONTENT via list even though hybrid/query correctly 403. These tests pin the
// gate closed (deny + allow), prove endpoint parity, and lock the per-record
// classification hide on the list path so list holds §5 compartmentation the
// same way the recall path does.
// ---------------------------------------------------------------------------

// blueAllowlist is an explicit DomainAccess policy that grants read/write on
// "blue-internal" only — it deliberately omits "red-internal" so the allowlist
// model denies it (checkDomainAccess returns the read-access error).
const blueAllowlist = `[{"domain":"blue-internal","read":true,"write":true}]`

// TestListMemories_DomainReadACL_DeniesUngrantedAgent is the exact reported
// repro: a blue agent with no read grant on "red-internal" must get 403 from
// list, not a 200 with the records' content.
func TestListMemories_DomainReadACL_DeniesUngrantedAgent(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, blueID := signedRequest(t, http.MethodGet,
		"/v1/memory/list?domain=red-internal&status=committed&limit=50", nil)

	// Blue is registered on-chain with an explicit allowlist that excludes
	// red-internal → checkDomainAccess denies the read.
	require.NoError(t, bs.RegisterAgent(blueID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(blueID, 1, blueAllowlist, "", "", ""))

	// Red owns red-internal and has a committed record there. The gate fires
	// before the store query, so the leak is closed regardless of submitter.
	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("red-internal", redID, "", 1))
	seedMemory(t, memStore, "r-secret", redID, "red-internal", "red operational detail")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code,
		"list must 403 for an agent with no read grant on the domain; body=%s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), "red-internal",
		"denial must name the domain, mirroring the hybrid/query message")
	assert.NotContains(t, rr.Body.String(), "red operational detail",
		"denied list must not leak any record content")
}

// TestListMemories_DomainReadACL_AllowsGrantedAgent is the positive control:
// the same shape of agent, but WITH read access to red-internal, gets 200 and
// its records back — the gate denies the ungranted, not everyone.
func TestListMemories_DomainReadACL_AllowsGrantedAgent(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, agentID := signedRequest(t, http.MethodGet,
		"/v1/memory/list?domain=red-internal&status=committed&limit=50", nil)

	// Allowlist that DOES grant read on red-internal.
	allow := `[{"domain":"red-internal","read":true,"write":true}]`
	require.NoError(t, bs.RegisterAgent(agentID, "red-member", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(agentID, 1, allow, "", "", ""))
	require.NoError(t, bs.RegisterDomain("red-internal", agentID, "", 1))

	seedMemory(t, memStore, "r-own", agentID, "red-internal", "authorized note")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "granted agent must read its domain; body=%s", rr.Body.String())

	var resp struct {
		Memories []struct {
			MemoryID string `json:"memory_id"`
		} `json:"memories"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Memories, 1, "granted agent must see the domain's record")
	assert.Equal(t, "r-own", resp.Memories[0].MemoryID)
}

// TestListMemories_DomainReadACL_ParityWithHybrid proves the asymmetry is gone:
// two agents with the same red-internal-excluding allowlist get 403 from BOTH
// list and hybrid. Before the fix, list returned 200 while hybrid returned 403.
func TestListMemories_DomainReadACL_ParityWithHybrid(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("red-internal", redID, "", 1))
	seedMemory(t, memStore, "r-secret", redID, "red-internal", "red operational detail")

	// list path
	listReq, listAgent := signedRequest(t, http.MethodGet,
		"/v1/memory/list?domain=red-internal&limit=50", nil)
	require.NoError(t, bs.RegisterAgent(listAgent, "blue-1", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(listAgent, 1, blueAllowlist, "", "", ""))
	listRR := httptest.NewRecorder()
	srv.Router().ServeHTTP(listRR, listReq)

	// hybrid path
	hybridBody, _ := json.Marshal(HybridSearchMemoryRequest{Query: "x", DomainTag: "red-internal", TopK: 10})
	hybridReq, hybridAgent := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", hybridBody)
	require.NoError(t, bs.RegisterAgent(hybridAgent, "blue-2", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(hybridAgent, 1, blueAllowlist, "", "", ""))
	hybridRR := httptest.NewRecorder()
	srv.Router().ServeHTTP(hybridRR, hybridReq)

	assert.Equal(t, http.StatusForbidden, hybridRR.Code, "hybrid 403 (unchanged)")
	assert.Equal(t, http.StatusForbidden, listRR.Code,
		"list must now 403 in parity with hybrid for the same ungranted agent")
}

// TestListMemories_ClassificationGate_HidesClassifiedRecord locks the per-record
// classification hide on the list path. A caller who can see all submitters
// (visible_agents="*") still must not receive the CONTENT of a record
// classified above their clearance — exactly as /query hides it. The caller's
// own records pass through; the hide is surfaced via header + envelope.
func TestListMemories_ClassificationGate_HidesClassifiedRecord(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet,
		"/v1/memory/list?domain=restricted.domain&limit=50", nil)

	// Caller sees all submitters (no submitting_agents filter), so the
	// classification gate is the only thing standing between them and the
	// classified record.
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, "", "*", "", ""))

	ownerID := "00000000000000000000000000000000000000000000000000000000000000bb"
	require.NoError(t, bs.RegisterAgent(ownerID, "owner", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("restricted.domain", ownerID, "", 1))

	// Owner's CONFIDENTIAL(2) record — caller has no multi-org path at that level.
	seedMemory(t, memStore, "m-classified", ownerID, "restricted.domain", "owner classified fact")
	require.NoError(t, bs.SetMemoryClassification("m-classified", 2))

	// Caller's own record passes the gate (submitter == caller).
	seedMemory(t, memStore, "m-own", callerID, "restricted.domain", "mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, filterByClassification, rr.Header().Get(filterHeader),
		"classification hide must surface in the filter header")

	var resp struct {
		Memories []struct {
			MemoryID string `json:"memory_id"`
			Content  string `json:"content"`
		} `json:"memories"`
		Filtered *FilterInfo `json:"filtered"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	require.NotNil(t, resp.Filtered)
	assert.Equal(t, []string{filterByClassification}, resp.Filtered.By)
	require.NotNil(t, resp.Filtered.HiddenCount)
	assert.Equal(t, 1, *resp.Filtered.HiddenCount, "one classified record hidden")

	require.Len(t, resp.Memories, 1, "only the caller's own record is returned")
	assert.Equal(t, "m-own", resp.Memories[0].MemoryID)
	for _, m := range resp.Memories {
		assert.NotEqual(t, "owner classified fact", m.Content, "classified content must not leak via list")
	}
}
