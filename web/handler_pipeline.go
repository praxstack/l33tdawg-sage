package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/l33tdawg/sage/internal/store"
)

// handlePipelineList returns recent pipeline messages for the dashboard.
func (h *DashboardHandler) handlePipelineList(w http.ResponseWriter, r *http.Request) {
	pipeStore, ok := h.store.(store.PipelineStore)
	if !ok {
		writeJSONDash(w, http.StatusOK, map[string]any{
			"items": []any{},
			"count": 0,
		})
		return
	}

	status := r.URL.Query().Get("status")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	items, err := pipeStore.ListPipelines(r.Context(), status, limit)
	if err != nil {
		writeJSONDash(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}
	if items == nil {
		items = []*store.PipelineMessage{}
	}

	writeJSONDash(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// handlePipelineStats returns pipeline status counts for the dashboard.
func (h *DashboardHandler) handlePipelineStats(w http.ResponseWriter, r *http.Request) {
	pipeStore, ok := h.store.(store.PipelineStore)
	if !ok {
		writeJSONDash(w, http.StatusOK, map[string]any{
			"stats": map[string]int{},
			"total": 0,
		})
		return
	}

	stats, err := pipeStore.PipelineStats(r.Context())
	if err != nil {
		writeJSONDash(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	total := 0
	for _, v := range stats {
		total += v
	}

	writeJSONDash(w, http.StatusOK, map[string]any{
		"stats": stats,
		"total": total,
	})
}

// writeJSONDash writes a JSON response (dashboard-specific to avoid name collision).
func writeJSONDash(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
