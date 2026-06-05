package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/memory"
)

// ---------------------------------------------------------------------------
// Per-domain read-ACL parity across the agent read surface.
//
// The original report named GET /v1/memory/list. The v10.1.1 review found the
// same compartmentation gap on three sibling reads that also key on `domain`:
//   - GET /v1/memory/tasks     (handleGetOpenTasks)   — leaked full task CONTENT
//   - GET /v1/memory/{id}      (handleGetMemory)      — DomainAccess allowlist bypass
//   - GET /v1/memory/timeline  (handleTimelineAuth)   — per-domain count metadata
// These tests pin the gate closed on each, plus add the load-bearing seeAll
// content-leak case the original list tests did not exercise.
// ---------------------------------------------------------------------------

// seedTask inserts a committed, open (in_progress) task-type memory.
func seedTask(t *testing.T, memStore *rbacMockMemoryStore, id, submitter, domain, content string) {
	t.Helper()
	memStore.memories[id] = &memory.MemoryRecord{
		MemoryID:        id,
		SubmittingAgent: submitter,
		Content:         content,
		ContentHash:     []byte(id),
		MemoryType:      memory.TypeTask,
		TaskStatus:      memory.TaskStatusInProgress,
		DomainTag:       domain,
		ConfidenceScore: 0.85,
		Status:          memory.StatusCommitted,
		CreatedAt:       time.Now().Add(-time.Hour),
	}
}

// TestListMemories_DomainReadACL_DeniesSeeAllCaller is the load-bearing content
// case the original deny tests missed: a caller that sees ALL submitters
// (visible_agents="*") but holds an allowlist excluding the queried owned
// domain. This is the ONLY configuration where list pre-fix actually returned
// another agent's CONTENT (submitter-isolation does not hide it, and a PUBLIC
// record dodges the classification gate). Layer A must 403 it.
func TestListMemories_DomainReadACL_DeniesSeeAllCaller(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet,
		"/v1/memory/list?domain=red-internal&limit=50", nil)

	// seeAll (visible_agents="*") AND an allowlist that excludes red-internal.
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("red-internal", redID, "", 1))
	seedMemory(t, memStore, "r-pub", redID, "red-internal", "red operational detail")
	require.NoError(t, bs.SetMemoryClassification("r-pub", 0)) // PUBLIC: dodges Layer B

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code,
		"seeAll caller with no read grant must 403, not enumerate content; body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "red operational detail",
		"denied list must not leak record content")
}

// TestGetOpenTasks_DomainReadACL_Denies pins the HIGH finding: an agent with no
// read grant on a domain must not read that domain's task CONTENT via the tasks
// endpoint.
func TestGetOpenTasks_DomainReadACL_Denies(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/tasks?domain=red-internal", nil)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("red-internal", redID, "", 1))
	seedTask(t, memStore, "t-red", redID, "red-internal", "RED TASK SECRET")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "RED TASK SECRET", "denied tasks must not leak content")
}

// TestGetOpenTasks_CrossDomain_FiltersUnreadable covers the no-`domain` board:
// with no domain param the handler returns tasks across domains, so the
// per-record domain-read filter must drop tasks in domains the caller cannot
// read while keeping those it can.
func TestGetOpenTasks_CrossDomain_FiltersUnreadable(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/tasks", nil)
	// Allowlist grants blue-internal, excludes red-internal.
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	seedTask(t, memStore, "t-red", redID, "red-internal", "RED TASK SECRET")
	seedTask(t, memStore, "t-blue", redID, "blue-internal", "blue task body")
	require.NoError(t, bs.SetMemoryClassification("t-red", 0))  // isolate the domain-read filter
	require.NoError(t, bs.SetMemoryClassification("t-blue", 0)) // from the classification gate

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "RED TASK SECRET",
		"cross-domain board must drop tasks in domains the caller cannot read")

	var resp struct {
		Tasks []struct {
			MemoryID string `json:"memory_id"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	ids := map[string]bool{}
	for _, tk := range resp.Tasks {
		ids[tk.MemoryID] = true
	}
	assert.True(t, ids["t-blue"], "task in a readable domain must be returned")
	assert.False(t, ids["t-red"], "task in an unreadable domain must be filtered out")
}

// TestGetOpenTasks_DomainReadACL_Allows is the positive control: a granted agent
// reads the domain's tasks normally.
func TestGetOpenTasks_DomainReadACL_Allows(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/tasks?domain=red-internal", nil)
	allow := `[{"domain":"red-internal","read":true,"write":true}]`
	require.NoError(t, bs.RegisterAgent(callerID, "red-member", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, allow, "*", "", ""))
	require.NoError(t, bs.RegisterDomain("red-internal", callerID, "", 1))

	otherID := "00000000000000000000000000000000000000000000000000000000000000aa"
	seedTask(t, memStore, "t-red", otherID, "red-internal", "authorized task")
	require.NoError(t, bs.SetMemoryClassification("t-red", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), "authorized task", "granted agent must read the domain's tasks")
}

// TestGetMemory_DomainReadACL_DeniesByID pins the GET-by-id allowlist gap: a
// seeAll caller (passes agent-isolation) with an allowlist excluding the
// record's UNOWNED domain could fetch the record one-by-one pre-fix (the
// multi-org gate is skipped for unowned domains). checkDomainAccess must 403 it.
func TestGetMemory_DomainReadACL_DeniesByID(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/m-secret", nil)
	// seeAll so agent-isolation passes; allowlist excludes secret-dom.
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	otherID := "00000000000000000000000000000000000000000000000000000000000000aa"
	// secret-dom intentionally NOT registered as owned → pre-fix the multi-org
	// gate is skipped and the record would return with content.
	seedMemory(t, memStore, "m-secret", otherID, "secret-dom", "by-id secret content")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code,
		"by-id fetch must honor the DomainAccess allowlist; body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "by-id secret content", "denied by-id fetch must not leak content")
}

// TestTimeline_DomainReadACL_Denies pins the timeline metadata gate: an agent
// with no read grant on a domain must not read that domain's submission-volume
// counts.
func TestTimeline_DomainReadACL_Denies(t *testing.T) {
	srv, _, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/timeline?domain=secret-dom", nil)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code,
		"timeline for an ungranted domain must 403; body=%s", rr.Body.String())
}

// TestTimeline_NoDomain_Allows confirms global (no-domain) timeline counts stay
// ungated — the gate only applies to a domain-scoped request.
func TestTimeline_NoDomain_Allows(t *testing.T) {
	srv, _, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/timeline", nil)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "global timeline must remain accessible; body=%s", rr.Body.String())
}

// TestListMemories_NoDomain_SeeAll_FiltersUnreadable pins the no-domain list
// path: a seeAll caller (visible_agents="*") with an allowlist excluding a
// domain must NOT receive that domain's PUBLIC content when listing with no
// domain param. The up-front gate only runs for ?domain=, so the per-record
// domain-read filter is the load-bearing protection here.
func TestListMemories_NoDomain_SeeAll_FiltersUnreadable(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/list?limit=50", nil) // NO domain
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", "")) // seeAll + allowlist

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	require.NoError(t, bs.RegisterAgent(redID, "red", "member", "", "test", "", 1))
	// PUBLIC records (class 0) so the classification gate is moot — the domain-read
	// filter is the only thing that can drop the red one.
	seedMemory(t, memStore, "r-red", redID, "red-internal", "RED PUBLIC SECRET")
	seedMemory(t, memStore, "r-blue", redID, "blue-internal", "blue public body")
	require.NoError(t, bs.SetMemoryClassification("r-red", 0))
	require.NoError(t, bs.SetMemoryClassification("r-blue", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "RED PUBLIC SECRET",
		"seeAll caller must not get PUBLIC content from a domain it cannot read")

	var resp struct {
		Memories []struct {
			MemoryID string `json:"memory_id"`
		} `json:"memories"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	ids := map[string]bool{}
	for _, m := range resp.Memories {
		ids[m.MemoryID] = true
	}
	assert.True(t, ids["r-blue"], "record in a readable domain must be returned")
	assert.False(t, ids["r-red"], "record in an unreadable domain must be filtered out")
}

// TestGetOpenTasks_ClassificationGate_HidesClassified pins the tasks per-record
// classification drop (which the domain-read tests deliberately bypass with
// class-0 records). A task classified above the caller's clearance, in a domain
// the caller CAN read, must be dropped while a readable task passes.
func TestGetOpenTasks_ClassificationGate_HidesClassified(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/memory/tasks?domain=restricted.domain", nil)
	// No explicit allowlist → checkDomainAccess permissive → domain authorized;
	// classification is then the only gate.
	require.NoError(t, bs.RegisterAgent(callerID, "caller", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, "", "*", "", ""))

	ownerID := "00000000000000000000000000000000000000000000000000000000000000bb"
	require.NoError(t, bs.RegisterAgent(ownerID, "owner", "member", "", "test", "", 1))
	require.NoError(t, bs.RegisterDomain("restricted.domain", ownerID, "", 1))

	seedTask(t, memStore, "t-classified", ownerID, "restricted.domain", "owner classified task")
	require.NoError(t, bs.SetMemoryClassification("t-classified", 2)) // above caller clearance
	seedTask(t, memStore, "t-mine", callerID, "restricted.domain", "my task")

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "owner classified task",
		"task classified above clearance must be dropped")

	var resp struct {
		Tasks []struct {
			MemoryID string `json:"memory_id"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	ids := map[string]bool{}
	for _, tk := range resp.Tasks {
		ids[tk.MemoryID] = true
	}
	assert.True(t, ids["t-mine"], "caller's own task must be returned")
	assert.False(t, ids["t-classified"], "classified task must be hidden")
}

// TestGetPending_DomainReadACL_Denies pins the pending endpoint: an agent with
// no read grant on a domain must not read that domain's PRE-COMMIT content.
func TestGetPending_DomainReadACL_Denies(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/validator/pending?domain_tag=red-internal", nil)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	memStore.pendingRecords = []*memory.MemoryRecord{{
		MemoryID: "p-red", SubmittingAgent: redID, Content: "PENDING RED SECRET",
		ContentHash: []byte("p-red"), MemoryType: memory.TypeFact, DomainTag: "red-internal",
		Status: memory.StatusProposed, CreatedAt: time.Now(),
	}}

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "PENDING RED SECRET", "denied pending must not leak content")
}

// TestGetPending_CrossDomain_FiltersUnreadable covers the all-domains fan-out
// (empty domain_tag → LIKE '%'): pending records in domains the caller cannot
// read must be dropped, those it can read kept.
func TestGetPending_CrossDomain_FiltersUnreadable(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/validator/pending", nil) // no domain_tag
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	memStore.pendingRecords = []*memory.MemoryRecord{
		{MemoryID: "p-red", SubmittingAgent: redID, Content: "PENDING RED SECRET", ContentHash: []byte("p-red"),
			MemoryType: memory.TypeFact, DomainTag: "red-internal", Status: memory.StatusProposed, CreatedAt: time.Now()},
		{MemoryID: "p-blue", SubmittingAgent: redID, Content: "pending blue body", ContentHash: []byte("p-blue"),
			MemoryType: memory.TypeFact, DomainTag: "blue-internal", Status: memory.StatusProposed, CreatedAt: time.Now()},
	}
	require.NoError(t, bs.SetMemoryClassification("p-red", 0))
	require.NoError(t, bs.SetMemoryClassification("p-blue", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "PENDING RED SECRET",
		"all-domains pending must drop content from unreadable domains")

	var resp PendingMemoriesResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	ids := map[string]bool{}
	for _, m := range resp.Memories {
		ids[m.MemoryID] = true
	}
	assert.True(t, ids["p-blue"], "pending in a readable domain must be returned")
	assert.False(t, ids["p-red"], "pending in an unreadable domain must be filtered out")
}

// TestQuery_NoDomain_SeeAll_FiltersUnreadable pins the recall path: a seeAll
// caller issuing a no-domain query must not receive PUBLIC content from a domain
// its allowlist excludes — the same compartmentation the list/tasks/pending
// fixes enforce, on the primary recall endpoint.
func TestQuery_NoDomain_SeeAll_FiltersUnreadable(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	body, _ := json.Marshal(QueryMemoryRequest{Embedding: embedding, TopK: 10}) // NO DomainTag
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/query", body)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", "")) // seeAll + allowlist

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	seedMemory(t, memStore, "q-red", redID, "red-internal", "RED PUBLIC SECRET")
	seedMemory(t, memStore, "q-blue", redID, "blue-internal", "blue public body")
	require.NoError(t, bs.SetMemoryClassification("q-red", 0))
	require.NoError(t, bs.SetMemoryClassification("q-blue", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "RED PUBLIC SECRET",
		"no-domain recall must not leak content from a domain the caller cannot read")
	assert.Contains(t, rr.Body.String(), "blue public body", "readable-domain content must still be returned")
}

// TestHybrid_NoDomain_SeeAll_FiltersUnreadable is the hybrid-path analogue.
func TestHybrid_NoDomain_SeeAll_FiltersUnreadable(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	embedding := make([]float32, 8)
	for i := range embedding {
		embedding[i] = 0.1
	}
	body, _ := json.Marshal(HybridSearchMemoryRequest{Query: "x", Embedding: embedding, TopK: 10}) // NO DomainTag
	req, callerID := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	require.NoError(t, bs.RegisterAgent(callerID, "blue", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, blueAllowlist, "*", "", ""))

	redID := "00000000000000000000000000000000000000000000000000000000000000aa"
	seedMemory(t, memStore, "h-red", redID, "red-internal", "RED PUBLIC SECRET")
	seedMemory(t, memStore, "h-blue", redID, "blue-internal", "blue public body")
	require.NoError(t, bs.SetMemoryClassification("h-red", 0))
	require.NoError(t, bs.SetMemoryClassification("h-blue", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.NotContains(t, rr.Body.String(), "RED PUBLIC SECRET",
		"no-domain hybrid recall must not leak content from an unreadable domain")
	assert.Contains(t, rr.Body.String(), "blue public body", "readable-domain content must still be returned")
}

// TestGetPending_DomainReadACL_Allows is the positive control: a granted agent
// reads the domain's pending records normally.
func TestGetPending_DomainReadACL_Allows(t *testing.T) {
	srv, memStore, bs, _ := newRBACTestServer(t)

	req, callerID := signedRequest(t, http.MethodGet, "/v1/validator/pending?domain_tag=red-internal", nil)
	allow := `[{"domain":"red-internal","read":true,"write":true}]`
	require.NoError(t, bs.RegisterAgent(callerID, "red-member", "member", "", "test", "", 1))
	require.NoError(t, bs.SetAgentPermission(callerID, 1, allow, "*", "", ""))

	otherID := "00000000000000000000000000000000000000000000000000000000000000aa"
	memStore.pendingRecords = []*memory.MemoryRecord{{
		MemoryID: "p-red", SubmittingAgent: otherID, Content: "authorized pending", ContentHash: []byte("p-red"),
		MemoryType: memory.TypeFact, DomainTag: "red-internal", Status: memory.StatusProposed, CreatedAt: time.Now(),
	}}
	require.NoError(t, bs.SetMemoryClassification("p-red", 0))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), "authorized pending", "granted agent must read the domain's pending records")
}
