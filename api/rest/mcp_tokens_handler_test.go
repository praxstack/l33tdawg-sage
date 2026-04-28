package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/store"
)

// newTokenServer wires a real SQLite store + Server for HTTP-level tests.
// We bypass the ed25519 admin auth in these tests by mounting the handlers
// directly on a chi router — the auth wrapping is exercised separately
// (handlers_test.go already covers Ed25519AuthMiddleware end-to-end).
func newTokenServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tokens.db")
	memStore, err := store.NewSQLiteStore(context.Background(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memStore.Close() })

	s := &Server{
		store:  memStore,
		logger: zerolog.Nop(),
	}

	r := chi.NewRouter()
	r.Post("/v1/mcp/tokens", s.handleMCPTokenIssue)
	r.Get("/v1/mcp/tokens", s.handleMCPTokenList)
	r.Delete("/v1/mcp/tokens/{id}", s.handleMCPTokenRevoke)
	return s, r
}

func TestMCPTokenIssue_Success(t *testing.T) {
	_, h := newTokenServer(t)

	pubkey := strings.Repeat("a", 64) // 64 hex chars = valid pubkey shape
	body := []byte(`{"agent_id":"` + pubkey + `","name":"chatgpt"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp/tokens", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)

	var resp MCPTokenIssueResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.NotEmpty(t, resp.ID)
	assert.NotEmpty(t, resp.Token, "token must be returned exactly once")
	assert.Equal(t, "chatgpt", resp.Name)
	assert.Equal(t, pubkey, resp.AgentID)
	assert.Contains(t, resp.UseHint, "Bearer")
}

func TestMCPTokenIssue_BadAgentID(t *testing.T) {
	_, h := newTokenServer(t)

	cases := []struct{ body, want string }{
		{`{"agent_id":""}`, "agent_id is required"},
		{`{"agent_id":"too-short"}`, "64-char hex"},
		{`{"agent_id":"` + strings.Repeat("z", 64) + `"}`, "hex-encoded"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/mcp/tokens", bytes.NewBufferString(c.body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "body=%s", c.body)
		assert.Contains(t, rr.Body.String(), c.want, "body=%s", c.body)
	}
}

func TestMCPTokenList_EmptyThenPopulated(t *testing.T) {
	_, h := newTokenServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/mcp/tokens", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var listResp struct {
		Tokens []MCPTokenSummary `json:"tokens"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&listResp))
	assert.Empty(t, listResp.Tokens)

	// Issue one, then list again.
	pubkey := strings.Repeat("b", 64)
	body := []byte(`{"agent_id":"` + pubkey + `","name":"cursor"}`)
	issueReq := httptest.NewRequest(http.MethodPost, "/v1/mcp/tokens", bytes.NewReader(body))
	issueRR := httptest.NewRecorder()
	h.ServeHTTP(issueRR, issueReq)
	require.Equal(t, http.StatusCreated, issueRR.Code)

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/v1/mcp/tokens", nil))
	require.Equal(t, http.StatusOK, rr2.Code)
	var listResp2 struct {
		Tokens []MCPTokenSummary `json:"tokens"`
	}
	require.NoError(t, json.NewDecoder(rr2.Body).Decode(&listResp2))
	require.Len(t, listResp2.Tokens, 1)
	assert.Equal(t, "cursor", listResp2.Tokens[0].Name)
	assert.Equal(t, pubkey, listResp2.Tokens[0].AgentID)
}

func TestMCPTokenRevoke(t *testing.T) {
	_, h := newTokenServer(t)

	pubkey := strings.Repeat("c", 64)
	body := []byte(`{"agent_id":"` + pubkey + `"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/mcp/tokens", bytes.NewReader(body)))
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp MCPTokenIssueResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))

	// Revoke it.
	revokeRR := httptest.NewRecorder()
	h.ServeHTTP(revokeRR, httptest.NewRequest(http.MethodDelete, "/v1/mcp/tokens/"+resp.ID, nil))
	assert.Equal(t, http.StatusNoContent, revokeRR.Code)

	// List should now show it as revoked.
	listRR := httptest.NewRecorder()
	h.ServeHTTP(listRR, httptest.NewRequest(http.MethodGet, "/v1/mcp/tokens", nil))
	var listResp struct {
		Tokens []MCPTokenSummary `json:"tokens"`
	}
	require.NoError(t, json.NewDecoder(listRR.Body).Decode(&listResp))
	require.Len(t, listResp.Tokens, 1)
	assert.False(t, listResp.Tokens[0].RevokedAt.IsZero())
}

func TestMCPTokenRevoke_NotFound(t *testing.T) {
	_, h := newTokenServer(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/v1/mcp/tokens/no-such-id", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
