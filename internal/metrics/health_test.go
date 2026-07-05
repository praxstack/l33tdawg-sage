package metrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func readiness(t *testing.T, h *HealthChecker, query string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ready"+query, nil)
	rec := httptest.NewRecorder()
	h.ReadinessHandler(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestReadiness_CoreDown_NotReady(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(false)
	code, body := readiness(t, h, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("core down must be 503, got %d", code)
	}
	if body["status"] != "not_ready" {
		t.Errorf("want not_ready, got %v", body["status"])
	}
}

func TestReadiness_SemanticEmbedderDown_Degraded(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(true)
	h.SetEmbedderHealth(EmbedderStatus{OK: false, Semantic: true, Provider: "ollama", Detail: "connection refused"})

	// Default: 200 degraded (node still serves keyword recall).
	code, body := readiness(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("semantic embedder down must be 200 degraded by default, got %d", code)
	}
	if body["status"] != "degraded" {
		t.Errorf("want degraded, got %v", body["status"])
	}

	// strict=1: hard 503 for gates that require semantic recall.
	code2, body2 := readiness(t, h, "?strict=1")
	if code2 != http.StatusServiceUnavailable {
		t.Fatalf("strict degraded must be 503, got %d", code2)
	}
	if body2["status"] != "degraded" {
		t.Errorf("want degraded, got %v", body2["status"])
	}
}

func TestReadiness_HashEmbedderDown_StillReady(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(true)
	// Hash provider is non-semantic: "down" is a capability note, not a fault.
	h.SetEmbedderHealth(EmbedderStatus{OK: false, Semantic: false})
	code, body := readiness(t, h, "")
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("hash provider must stay ready, got %d %v", code, body["status"])
	}
}

func TestReadiness_CoreDownTakesPrecedenceOverEmbedder(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(false) // core down
	h.SetEmbedderHealth(EmbedderStatus{OK: true, Semantic: true})
	code, body := readiness(t, h, "")
	if code != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("core-down must win over a healthy embedder, got %d %v", code, body["status"])
	}
}

func TestReadiness_SemanticEmbedderUp_Ready(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(true)
	h.SetEmbedderHealth(EmbedderStatus{OK: true, Semantic: true, Provider: "ollama", Model: "nomic-embed-text"})
	code, body := readiness(t, h, "")
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("healthy semantic embedder must be ready, got %d %v", code, body["status"])
	}
}

func TestReadiness_EmbedderUnchecked_Ready(t *testing.T) {
	h := NewHealthChecker()
	h.SetPostgresHealth(true)
	h.SetCometBFTHealth(true)
	// Never probed (watchdog hasn't run): must not block readiness.
	code, body := readiness(t, h, "")
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("unchecked embedder must be ready, got %d %v", code, body["status"])
	}
	emb, _ := body["embedder"].(map[string]any)
	if emb == nil || emb["checked"] != false {
		t.Errorf("embedder block should report checked=false, got %v", body["embedder"])
	}
}
