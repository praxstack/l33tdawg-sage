package mcp

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreMemory_SubmitsWithoutVectorWhenEmbedderDown guards the sage_turn store
// resilience: when the embedder is genuinely unavailable (Ollama down/unreachable,
// after /v1/embed's own retries), storeMemory must still commit the observation —
// without a vector — instead of returning an error that surfaces as a flaky
// sage_turn store_error. The memory commits and is re-embeddable (not semantically
// recallable until a re-embed backfills the vector), and reports degraded=true.
func TestStoreMemory_SubmitsWithoutVectorWhenEmbedderDown(t *testing.T) {
	withFastBackoffs(t)

	submitCalled := false
	var submittedEmbedding any = "unset"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/memory/pre-validate", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
	})
	mux.HandleFunc("/v1/embed", func(w http.ResponseWriter, _ *http.Request) {
		// Embedder genuinely unavailable — /v1/embed returns an error.
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "embed error", "detail": "ollama unreachable"})
	})
	mux.HandleFunc("/v1/memory/submit", func(w http.ResponseWriter, r *http.Request) {
		submitCalled = true
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		submittedEmbedding = body["embedding"]
		_ = json.NewEncoder(w).Encode(map[string]any{"memory_id": "m1", "status": "proposed"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	s := NewServer(ts.URL, priv)

	degraded, err := s.storeMemory(context.Background(), "a durable observation worth keeping", "general", "observation", 0.80)
	require.NoError(t, err, "an embedder outage must NOT drop the observation — store it without a vector")
	assert.True(t, submitCalled, "the memory must still be submitted")
	assert.Nil(t, submittedEmbedding, "embedding must be omitted/null when the embedder is down")
	assert.True(t, degraded, "a vectorless store must report degraded=true so the turn can surface it")
}
