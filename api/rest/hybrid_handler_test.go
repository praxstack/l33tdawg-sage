package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/l33tdawg/sage/internal/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHybridSearchMemory_Success(t *testing.T) {
	srv, memStore, _ := newTestServer(t, "")

	memStore.memories["hybrid-1"] = &memory.MemoryRecord{
		MemoryID:        "hybrid-1",
		SubmittingAgent: "agent-x",
		Content:         "fused result",
		ContentHash:     []byte{1, 2, 3},
		MemoryType:      memory.TypeFact,
		DomainTag:       "general",
		ConfidenceScore: 0.9,
		Status:          memory.StatusCommitted,
		CreatedAt:       time.Now().Add(-time.Hour),
	}

	body, _ := json.Marshal(HybridSearchMemoryRequest{
		Query:     "fused",
		Embedding: []float32{0.1, 0.2, 0.3},
		TopK:      5,
	})

	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "expected 200, got %d: %s", rr.Code, rr.Body.String())

	var resp QueryMemoryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.GreaterOrEqual(t, resp.TotalCount, 1)
	assert.Equal(t, "hybrid-1", resp.Results[0].MemoryID)
}

func TestHybridSearchMemory_RequiresQueryOrEmbedding(t *testing.T) {
	srv, _, _ := newTestServer(t, "")

	body := []byte(`{"top_k":5}`) // no query, no embedding
	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "hybrid search requires at least one")
}

func TestHybridSearchMemory_AcceptsEmbeddingOnly(t *testing.T) {
	srv, memStore, _ := newTestServer(t, "")

	memStore.memories["vec-only"] = &memory.MemoryRecord{
		MemoryID:        "vec-only",
		SubmittingAgent: "agent-x",
		Content:         "no text query path",
		ContentHash:     []byte{4, 5, 6},
		MemoryType:      memory.TypeObservation,
		DomainTag:       "general",
		ConfidenceScore: 0.8,
		Status:          memory.StatusCommitted,
		CreatedAt:       time.Now(),
	}

	body := []byte(`{"embedding":[0.1,0.2,0.3],"top_k":3}`)
	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "embedding-only should be accepted")
}

func TestHybridSearchMemory_AcceptsQueryOnly(t *testing.T) {
	srv, memStore, _ := newTestServer(t, "")

	memStore.memories["txt-only"] = &memory.MemoryRecord{
		MemoryID:        "txt-only",
		SubmittingAgent: "agent-x",
		Content:         "bm25 path",
		ContentHash:     []byte{7, 8, 9},
		MemoryType:      memory.TypeObservation,
		DomainTag:       "general",
		ConfidenceScore: 0.7,
		Status:          memory.StatusCommitted,
		CreatedAt:       time.Now(),
	}

	body := []byte(`{"query":"bm25","top_k":3}`)
	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "query-only should be accepted (RRF degrades to BM25-only)")
}

func TestHybridSearchMemory_PlumbsTagsThroughToStore(t *testing.T) {
	srv, memStore, _ := newTestServer(t, "")

	body := []byte(`{"query":"x","embedding":[0.1,0.2,0.3],"top_k":5,"tags":["alpha","beta"]}`)
	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", body)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "expected 200, got %d: %s", rr.Code, rr.Body.String())
	assert.Equal(t, []string{"alpha", "beta"}, memStore.lastQueryTags,
		"hybrid handler must forward tags through to the store layer")
}

func TestHybridSearchMemory_InvalidJSONRejected(t *testing.T) {
	srv, _, _ := newTestServer(t, "")

	req, _ := signedRequest(t, http.MethodPost, "/v1/memory/hybrid", []byte(`{not-json`))
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
