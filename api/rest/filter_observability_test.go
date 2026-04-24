package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/tx"
)

// ---------------------------------------------------------------------------
// v6.6.2 item 1: silent-filter observability
//
// When the RBAC submitting_agents filter or the classification+multi-org
// per-record filter hides data from the caller, the response must surface
// the fact - via `X-SAGE-Filter-Applied` response header and a `filtered`
// field in the JSON body - so clients can distinguish "empty domain" from
// "access-limited result".
// ---------------------------------------------------------------------------

// TestFilterObservability_List_FilterApplied exercises the /v1/memory/list
// endpoint with a caller whose resolveVisibleAgents returns seeAll=false and
// no grant on the queried domain. The RBAC submitting_agents filter fires;
// response must carry the header and envelope with total_before_filter/visible.
func TestFilterObservability_List_FilterApplied(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/list?domain=shared.secret&limit=50", nil)

	// Caller is NOT registered on-chain - resolveVisibleAgents falls through to
	// SQLite fallback. The SQLite mock has no record either, so allowedAgents =
	// [callerID], seeAll=false. Keep caller unregistered so the list path's
	// grant-aware override cannot flip seeAll to true.

	// Seed owner + domain + grants so the grant-aware overrides don't trigger.
	ownerID := "0000000000000000000000000000000000000000000000000000000000000001"
	require.NoError(t, bs.RegisterAgent(ownerID, "owner", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("shared.secret", ownerID, "", 1))
	require.NoError(t, bs.SetAccessGrant("shared.secret", ownerID, 2, 0, ownerID))

	// Three memories in the domain, only one submitted by the caller.
	seedMemory(t, memStore, "m-own", callerID, "shared.secret", "mine")
	seedMemory(t, memStore, "m-hidden-1", ownerID, "shared.secret", "not mine")
	seedMemory(t, memStore, "m-hidden-2", ownerID, "shared.secret", "also not mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, filterBySubmittingAgts, rr.Header().Get(filterHeader),
		"header must announce that the submitting_agents filter was applied")

	var resp struct {
		Memories []any        `json:"memories"`
		Total    int          `json:"total"`
		Filtered *FilterInfo  `json:"filtered"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	require.NotNil(t, resp.Filtered, "envelope must include filtered block when filter applies")
	assert.Equal(t, []string{filterBySubmittingAgts}, resp.Filtered.By)
	require.NotNil(t, resp.Filtered.TotalBeforeFilter)
	require.NotNil(t, resp.Filtered.Visible)
	assert.Equal(t, 3, *resp.Filtered.TotalBeforeFilter, "total_before_filter must count all memories in domain")
	assert.Equal(t, 1, *resp.Filtered.Visible, "visible must count only caller-submitted memories")
	assert.Equal(t, 1, resp.Total, "legacy total field must match filtered visible count")
}

// TestFilterObservability_List_NoFilter verifies that when the caller has
// see-all visibility (wildcard), no header and no filtered envelope are emitted.
func TestFilterObservability_List_NoFilter(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/list?domain=shared.secret&limit=50", nil)

	// Caller has visible_agents="*" - resolveVisibleAgents returns seeAll=true.
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, "", "*", "", ""))

	seedMemory(t, memStore, "m1", callerID, "shared.secret", "content")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Empty(t, rr.Header().Get(filterHeader), "no filter means no header")

	var resp struct {
		Filtered *FilterInfo `json:"filtered"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Nil(t, resp.Filtered, "no filter means no envelope")
}

// TestFilterObservability_Query_SubmittingAgentsFilter verifies /v1/memory/query
// surfaces the submitting_agents filter when it applies.
// The filter only fires when req.DomainTag is empty (a domain-scoped query
// passes checkDomainAccess which flips seeAll=true).
func TestFilterObservability_Query_SubmittingAgentsFilter(t *testing.T) {
	srv, memStore, _, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	// No DomainTag → checkDomainAccess is skipped → seeAll stays false.
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, TopK: 10})
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)

	// Caller intentionally unregistered - resolveVisibleAgents returns seeAll=false.

	// One of caller's memories and one of someone else's.
	otherID := "0000000000000000000000000000000000000000000000000000000000000002"
	seedMemory(t, memStore, "m-own", callerID, "anydomain", "mine")
	seedMemory(t, memStore, "m-other", otherID, "anydomain", "not mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, filterBySubmittingAgts, rr.Header().Get(filterHeader))

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Filtered)
	assert.Equal(t, []string{filterBySubmittingAgts}, resp.Filtered.By)
	// No total-before-filter on /query (topk-bounded, no unbounded total).
	assert.Nil(t, resp.Filtered.TotalBeforeFilter)
}

// TestFilterObservability_Query_ClassificationFilter verifies the per-record
// classification+multi-org filter surfaces in the envelope when it hides
// records - including the case where the submitting_agents filter does NOT
// apply (domain-scoped query that the caller is authorized to read at the
// domain level but not cleared for a specific memory's classification).
func TestFilterObservability_Query_ClassificationFilter(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, DomainTag: "restricted.domain", TopK: 10})
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)

	// Caller registered on-chain - passes checkDomainAccess (no DomainAccess
	// restrictions configured) so seeAll flips to true. No submitting_agents
	// filter. Classification filter still runs per-record.
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))

	// Domain has a registered owner (required for classification filter to engage).
	ownerID := "0000000000000000000000000000000000000000000000000000000000000003"
	require.NoError(t, bs.RegisterAgent(ownerID, "owner", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("restricted.domain", ownerID, "", 1))

	// Set classification=2 (CONFIDENTIAL) on the owner's memory. Caller has no
	// multi-org access at that level, so the in-loop filter drops it.
	seedMemory(t, memStore, "m-classified", ownerID, "restricted.domain", "owner's classified fact")
	require.NoError(t, bs.SetMemoryClassification("m-classified", 2))

	// Caller's own memory passes through (rec.SubmittingAgent == queryAgentID).
	seedMemory(t, memStore, "m-own", callerID, "restricted.domain", "mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, filterByClassification, rr.Header().Get(filterHeader))

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Filtered)
	assert.Equal(t, []string{filterByClassification}, resp.Filtered.By)
	require.NotNil(t, resp.Filtered.HiddenCount)
	assert.Equal(t, 1, *resp.Filtered.HiddenCount, "one memory hidden by classification")
}

// ---------------------------------------------------------------------------
// v6.6.2 item 2: org-clearance-as-seeAll
//
// A TopSecret member of an org should bypass the submitting_agents filter
// without needing visible_agents="*" explicitly. Closes homogeneous-trust
// boilerplate for single-org deployments. Per-domain access control still
// applies - this only lifts the submitting_agents filter.
// ---------------------------------------------------------------------------

func TestOrgClearance_TopSecretBypassesSubmittingAgentsFilter(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	// Omit DomainTag so checkDomainAccess doesn't flip seeAll=true unrelated
	// to clearance - this isolates the new clearance path.
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, TopK: 10})
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)

	// Caller is a TopSecret member of org "acme".
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterOrg("acme", "Acme Inc", "", callerID, 1))
	require.NoError(t, bs.AddOrgMember("acme", callerID, uint8(tx.ClearanceTopSecret), "member", 1))

	otherID := "0000000000000000000000000000000000000000000000000000000000000002"
	seedMemory(t, memStore, "m-other", otherID, "anydomain", "not mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.TotalCount, "TopSecret clearance must lift agent isolation - caller sees other agents' memories")
	assert.Nil(t, resp.Filtered, "no filter applied means no envelope")
	assert.Empty(t, rr.Header().Get(filterHeader), "no filter applied means no header")
}

func TestOrgClearance_InternalClearanceStillFiltered(t *testing.T) {
	// Negative control: sub-TopSecret clearance must NOT bypass the filter.
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, TopK: 10})
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)

	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterOrg("acme", "Acme Inc", "", callerID, 1))
	// Internal clearance (1) - well below TopSecret (4).
	require.NoError(t, bs.AddOrgMember("acme", callerID, uint8(tx.ClearanceInternal), "member", 1))

	otherID := "0000000000000000000000000000000000000000000000000000000000000002"
	seedMemory(t, memStore, "m-other", otherID, "anydomain", "not mine")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.TotalCount, "internal clearance must NOT bypass filter")
	require.NotNil(t, resp.Filtered)
	assert.Contains(t, resp.Filtered.By, filterBySubmittingAgts)
}

// TestFilterObservability_Query_NoFilter verifies clean response when no
// filter runs - caller sees everything, no envelope, no header.
func TestFilterObservability_Query_NoFilter(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, DomainTag: "open.domain", TopK: 10})
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)

	// Caller registered with wildcard visibility → resolveVisibleAgents seeAll=true.
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, "", "*", "", ""))

	seedMemory(t, memStore, "m1", callerID, "open.domain", "content")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Empty(t, rr.Header().Get(filterHeader))

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Nil(t, resp.Filtered)
}
