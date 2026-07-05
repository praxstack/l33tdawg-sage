package metrics

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// EmbedderStatus is the health checker's view of the embedding provider, refreshed by
// the node's background watchdog. It lets /ready report whether SEMANTIC recall is
// actually available — a down embedder silently degrades hybrid recall to keyword-only.
type EmbedderStatus struct {
	Checked  bool   `json:"checked"`            // has the watchdog probed yet?
	OK       bool   `json:"ok"`                 // reachable this probe
	Semantic bool   `json:"semantic"`           // true=meaning-based (Ollama/…); false=hash fallback
	Provider string `json:"provider,omitempty"` // e.g. "ollama"
	Model    string `json:"model,omitempty"`    // e.g. "nomic-embed-text"
	Detail   string `json:"detail,omitempty"`   // error summary when down
}

// HealthChecker tracks the health status of dependencies.
type HealthChecker struct {
	postgresOK atomic.Bool
	cometbftOK atomic.Bool
	embedder   atomic.Value // EmbedderStatus, set by SetEmbedderHealth
	Version    string
}

// NewHealthChecker creates a new health checker.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

// SetEmbedderHealth records the latest embedding-provider probe (called by the node's
// watchdog). Until the first call, the embedder reads as not-yet-checked.
func (h *HealthChecker) SetEmbedderHealth(s EmbedderStatus) {
	s.Checked = true
	h.embedder.Store(s)
}

func (h *HealthChecker) embedderStatus() EmbedderStatus {
	if v, ok := h.embedder.Load().(EmbedderStatus); ok {
		return v
	}
	return EmbedderStatus{}
}

// SetPostgresHealth updates the PostgreSQL health status.
func (h *HealthChecker) SetPostgresHealth(ok bool) {
	h.postgresOK.Store(ok)
}

// SetCometBFTHealth updates the CometBFT health status.
func (h *HealthChecker) SetCometBFTHealth(ok bool) {
	h.cometbftOK.Store(ok)
}

// IsHealthy returns true if all dependencies are healthy.
func (h *HealthChecker) IsHealthy() bool {
	return h.postgresOK.Load() && h.cometbftOK.Load()
}

// HealthHandler handles GET /health requests.
func (h *HealthChecker) HealthHandler(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	httpStatus := http.StatusOK

	if !h.IsHealthy() {
		status = "unhealthy"
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	// /health is reachable through the wizard's tunnel allowlist; we keep
	// it minimal so internet visitors can't easily fingerprint a SAGE node
	// to a specific version.
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status,
	})
}

// ReadinessHandler handles GET /ready requests.
func (h *HealthChecker) ReadinessHandler(w http.ResponseWriter, r *http.Request) {
	pgOK := h.postgresOK.Load()
	cmtOK := h.cometbftOK.Load()
	emb := h.embedderStatus()

	status := "ready"
	httpStatus := http.StatusOK

	switch {
	case !pgOK || !cmtOK:
		// Core infrastructure down — genuinely not ready.
		status = "not_ready"
		httpStatus = http.StatusServiceUnavailable
	case emb.Checked && emb.Semantic && !emb.OK:
		// A semantic embedder that has been probed and is down: the node still SERVES
		// (keyword recall works) but semantic/hybrid recall is unavailable. Report
		// "degraded" with HTTP 200 by default so orchestrators pick their own
		// strictness; ?strict=1 makes it a hard 503 for readiness gates that require
		// semantic recall. A hash provider (Semantic=false) is a capability, not a
		// fault, so it stays "ready".
		status = "degraded"
		if r.URL.Query().Get("strict") == "1" {
			httpStatus = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   status,
		"postgres": pgOK,
		"cometbft": cmtOK,
		"embedder": emb,
	})
}
