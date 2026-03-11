package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/memory"
	"github.com/l33tdawg/sage/internal/store"
)

func newTestHandler(t *testing.T) (*DashboardHandler, *store.SQLiteStore) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLiteStore(context.Background(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	h := NewDashboardHandler(s, "test")
	return h, s
}

func testRouter(h *DashboardHandler) *chi.Mux {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func insertTestMemory(t *testing.T, s *store.SQLiteStore, id, domain string) {
	t.Helper()
	h := sha256.Sum256([]byte("content-" + id))
	rec := &memory.MemoryRecord{
		MemoryID:        id,
		SubmittingAgent: "agent1",
		Content:         "content-" + id,
		ContentHash:     h[:],
		MemoryType:      memory.TypeObservation,
		DomainTag:       domain,
		ConfidenceScore: 0.85,
		Status:          memory.StatusProposed,
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, s.InsertMemory(context.Background(), rec))
}

func TestHandleListMemories(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	for i := 0; i < 5; i++ {
		insertTestMemory(t, s, fmt.Sprintf("m%d", i), "general")
	}

	req := httptest.NewRequest("GET", "/v1/dashboard/memory/list?limit=3", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(5), resp["total"])
	memories := resp["memories"].([]any)
	assert.Len(t, memories, 3)
}

func TestHandleStats(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	insertTestMemory(t, s, "m1", "security")
	insertTestMemory(t, s, "m2", "general")

	req := httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["total_memories"])
}

func TestHandleDeleteMemory(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	insertTestMemory(t, s, "m1", "general")

	req := httptest.NewRequest("DELETE", "/v1/dashboard/memory/m1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "deleted", resp["status"])

	// Verify in store
	got, err := s.GetMemory(context.Background(), "m1")
	require.NoError(t, err)
	assert.Equal(t, memory.StatusDeprecated, got.Status)
}

func TestHandleUpdateMemory(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	insertTestMemory(t, s, "m1", "general")

	body, _ := json.Marshal(map[string]string{"domain": "security"})
	req := httptest.NewRequest("PATCH", "/v1/dashboard/memory/m1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	got, err := s.GetMemory(context.Background(), "m1")
	require.NoError(t, err)
	assert.Equal(t, "security", got.DomainTag)
}

func TestHandleUpdateMemory_MissingDomain(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest("PATCH", "/v1/dashboard/memory/m1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleGraph(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	insertTestMemory(t, s, "m1", "general")
	insertTestMemory(t, s, "m2", "general")
	insertTestMemory(t, s, "m3", "security")

	req := httptest.NewRequest("GET", "/v1/dashboard/memory/graph", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	nodes := resp["nodes"].([]any)
	assert.Len(t, nodes, 3)
	// Should have domain edges (2 general memories = 1 domain edge)
	edges := resp["edges"].([]any)
	assert.GreaterOrEqual(t, len(edges), 1)
}

func TestHandleAuthCheck_NoAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/v1/dashboard/auth/check", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["auth_required"])
	assert.Equal(t, true, resp["authenticated"])
}

func TestHandleAuthMiddleware_BlocksWithoutSession(t *testing.T) {
	h, _ := newTestHandler(t)
	// Simulate encryption enabled
	h.VaultKeyPath = "/tmp/fake-vault.key"
	h.Encrypted = true
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "unauthorized", resp["error"])
	assert.Equal(t, true, resp["login_required"])
}

func TestHandleExport_AllMemories(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	// Insert more than the list endpoint's 200 cap
	for i := 0; i < 5; i++ {
		insertTestMemory(t, s, fmt.Sprintf("export-%d", i), "test-domain")
	}

	req := httptest.NewRequest("GET", "/v1/dashboard/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/x-ndjson")
	assert.Contains(t, w.Header().Get("Content-Disposition"), "sage-backup-")

	// Parse JSONL lines
	lines := bytes.Split(bytes.TrimSpace(w.Body.Bytes()), []byte("\n"))
	assert.Len(t, lines, 5)

	// Verify each line is valid JSON with expected fields
	for _, line := range lines {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		assert.NotEmpty(t, rec["memory_id"])
		assert.NotEmpty(t, rec["content"])
		assert.Equal(t, "test-domain", rec["domain_tag"])
		// Embeddings should be excluded
		assert.Nil(t, rec["embedding"])
	}
}

func TestHandleExport_PaginatesAcrossPages(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	// Insert more than the internal page size (500) to force multiple pages.
	totalRecords := 520
	for i := 0; i < totalRecords; i++ {
		insertTestMemory(t, s, fmt.Sprintf("page-%04d", i), "bulk-domain")
	}

	req := httptest.NewRequest("GET", "/v1/dashboard/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Parse JSONL lines — should have ALL records, not capped at 500
	lines := bytes.Split(bytes.TrimSpace(w.Body.Bytes()), []byte("\n"))
	assert.Len(t, lines, totalRecords)

	// Verify first and last records are valid JSON
	var first, last map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &first))
	require.NoError(t, json.Unmarshal(lines[totalRecords-1], &last))
	assert.NotEmpty(t, first["memory_id"])
	assert.NotEmpty(t, last["memory_id"])
	assert.Equal(t, "bulk-domain", first["domain_tag"])
	assert.Equal(t, "bulk-domain", last["domain_tag"])
}

func TestHandleExport_EmptyDB(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/v1/dashboard/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/x-ndjson")
	// Body should be empty (no records)
	assert.Empty(t, bytes.TrimSpace(w.Body.Bytes()))
}

func TestHandleAuthMiddleware_DynamicEncryption(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	// Encryption OFF — should allow access
	req := httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Enable encryption dynamically (simulates enabling via dashboard)
	h.Encrypted = true

	// Same request without cookie — should be blocked
	req = httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Disable encryption — should allow again
	h.Encrypted = false
	req = httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleLock_InvalidatesSession(t *testing.T) {
	h, _ := newTestHandler(t)
	h.VaultKeyPath = filepath.Join(t.TempDir(), "vault.key")
	r := testRouter(h)

	// Enable encryption and login
	body, _ := json.Marshal(map[string]string{"passphrase": "test-pass"})
	req := httptest.NewRequest("POST", "/v1/dashboard/settings/ledger/enable", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Login to get session
	body, _ = json.Marshal(map[string]string{"passphrase": "test-pass"})
	req = httptest.NewRequest("POST", "/v1/dashboard/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies)
	sessionCookie := cookies[0]

	// Verify session works
	req = httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Lock — invalidates the session
	req = httptest.NewRequest("POST", "/v1/dashboard/auth/lock", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var lockResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &lockResp))
	assert.Equal(t, true, lockResp["locked"])

	// Same session cookie should now be rejected
	req = httptest.NewRequest("GET", "/v1/dashboard/stats", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleTimeline(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	insertTestMemory(t, s, "m1", "general")

	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	url := fmt.Sprintf("/v1/dashboard/memory/timeline?from=%s&to=%s&bucket=hour", from, to)

	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "buckets")
}

// ---------------------------------------------------------------------------
// Network / Template tests
// ---------------------------------------------------------------------------

func TestHandleTemplates(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/v1/dashboard/network/templates", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Templates []struct {
			Name      string `json:"name"`
			Role      string `json:"role"`
			Bio       string `json:"bio"`
			Clearance int    `json:"clearance"`
			Avatar    string `json:"avatar"`
		} `json:"templates"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, len(resp.Templates), 2, "should have multiple templates")

	// Verify Coding Assistant template is present
	found := false
	for _, tmpl := range resp.Templates {
		if tmpl.Name == "Coding Assistant" {
			found = true
			assert.Equal(t, "member", tmpl.Role)
			assert.NotEmpty(t, tmpl.Bio)
			assert.Equal(t, 1, tmpl.Clearance)
			break
		}
	}
	assert.True(t, found, "Coding Assistant template should be present")
}

func TestHandleTemplatesContainsExpected(t *testing.T) {
	h, _ := newTestHandler(t)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/v1/dashboard/network/templates", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp struct {
		Templates []struct {
			Name string `json:"name"`
		} `json:"templates"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	names := make([]string, len(resp.Templates))
	for i, t := range resp.Templates {
		names[i] = t.Name
	}
	assert.Contains(t, names, "Coding Assistant")
	assert.Contains(t, names, "Voice Assistant")
	assert.Contains(t, names, "Research Agent")
	assert.Contains(t, names, "Custom")
}

// ---------------------------------------------------------------------------
// Unregistered agents test
// ---------------------------------------------------------------------------

func insertTestMemoryWithAgent(t *testing.T, s *store.SQLiteStore, id, domain, agentID string) {
	t.Helper()
	h := sha256.Sum256([]byte("content-" + id))
	rec := &memory.MemoryRecord{
		MemoryID:        id,
		SubmittingAgent: agentID,
		Content:         "content-" + id,
		ContentHash:     h[:],
		MemoryType:      memory.TypeObservation,
		DomainTag:       domain,
		ConfidenceScore: 0.85,
		Status:          memory.StatusProposed,
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, s.InsertMemory(context.Background(), rec))
}

func TestHandleUnregisteredAgents(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	// Create an agent in the dashboard
	agent := &store.AgentEntry{
		AgentID:   "registered-agent-id",
		Name:      "Registered Agent",
		Role:      "admin",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, s.CreateAgent(context.Background(), agent))

	// Insert memories from the registered agent
	insertTestMemoryWithAgent(t, s, "m1", "general", "registered-agent-id")

	// Insert memories from an unregistered agent
	insertTestMemoryWithAgent(t, s, "m2", "general", "orphan-agent-id")
	insertTestMemoryWithAgent(t, s, "m3", "security", "orphan-agent-id")

	req := httptest.NewRequest("GET", "/v1/dashboard/network/unregistered", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Unregistered []struct {
			AgentID     string `json:"agent_id"`
			MemoryCount int    `json:"memory_count"`
			ShortID     string `json:"short_id"`
		} `json:"unregistered"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Unregistered, 1, "should find exactly one unregistered agent")
	assert.Equal(t, "orphan-agent-id", resp.Unregistered[0].AgentID)
	assert.Equal(t, 2, resp.Unregistered[0].MemoryCount)
	assert.NotEmpty(t, resp.Unregistered[0].ShortID)
}

func TestHandleUnregisteredAgents_NoneOrphan(t *testing.T) {
	h, s := newTestHandler(t)
	r := testRouter(h)

	// Create an agent and memories from only that agent
	agent := &store.AgentEntry{
		AgentID:   "only-agent",
		Name:      "Only Agent",
		Role:      "admin",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, s.CreateAgent(context.Background(), agent))
	insertTestMemoryWithAgent(t, s, "m1", "general", "only-agent")

	req := httptest.NewRequest("GET", "/v1/dashboard/network/unregistered", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Unregistered []any `json:"unregistered"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Unregistered, 0, "no orphaned agents should be found")
}
