package web

// Embeddings setup (Phase: embeddings-setup + re-embed). SAGE ships with a
// bundled local embedder (Ollama + nomic-embed-text) but falls back to a "hash"
// pseudo-embedder when Ollama isn't the configured provider — which gives only
// keyword matching, no semantic recall. This surface lets the operator turn the
// real embedder on from CEREBRUM: detect/guide Ollama, re-embed all existing
// memories through it (SSE progress), then switch the node to the Ollama
// provider (a restart, so every consumer picks it up).
//
// The embedder is LOCKED to Ollama + nomic-embed-text — this is not a
// "choose your embedder" screen; it just turns the bundled one on.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/l33tdawg/sage/internal/embedding"
)

const (
	ollamaBaseURL   = "http://localhost:11434"
	embedModel      = "nomic-embed-text"
	embedDimension  = 768
	reembedPageSize = 100
)

// RegisterEmbeddingsRoutes wires the embeddings-setup routes (authed group).
func (h *DashboardHandler) RegisterEmbeddingsRoutes(r chi.Router) {
	r.Get("/v1/dashboard/embeddings/status", h.handleEmbeddingsStatus)
	r.Post("/v1/dashboard/embeddings/check-ollama", h.handleEmbeddingsCheckOllama)
	r.Post("/v1/dashboard/embeddings/pull-model", h.handleEmbeddingsPullModel)
	r.Post("/v1/dashboard/embeddings/reembed", h.handleEmbeddingsReembed)
	r.Post("/v1/dashboard/embeddings/enable", h.handleEmbeddingsEnable)
}

// handleEmbeddingsStatus reports the current embedder + how much re-embedding is
// pending, so the frontend can drive the setup flow and surface a "turn on
// semantic search" call to action from the hash-provider status.
func (h *DashboardHandler) handleEmbeddingsStatus(w http.ResponseWriter, r *http.Request) {
	counts, err := h.store.CountMemoriesByProvider(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count memories: "+err.Error())
		return
	}
	total, onOllama := 0, 0
	for provider, n := range counts {
		total += n
		if provider == embedProviderOllama {
			onOllama += n
		}
	}
	current := currentEmbedProvider(h.embedder)
	ollamaUp := ollamaRunning(r.Context())
	modelReady := ollamaUp && ollamaHasModel(r.Context(), embedModel)

	writeJSONResp(w, http.StatusOK, map[string]any{
		"provider":        current,            // active embedder: "hash" | "ollama" | ...
		"is_semantic":     current == embedProviderOllama,
		"model":           embedModel,
		"dimension":       embedDimension,
		"ollama_running":  ollamaUp,
		"model_available": modelReady,
		"total_memories":  total,
		"need_reembed":    total - onOllama, // memories not yet on the Ollama vector space
		"on_ollama":       onOllama,
		"vault_locked":    h.VaultLocked.Load(),
	})
}

const embedProviderOllama = "ollama"

// currentEmbedProvider mirrors the health handler's provider dispatch.
func currentEmbedProvider(e Embedder) string {
	if e == nil {
		return "unknown"
	}
	if named, ok := e.(embedding.Named); ok {
		return named.Name()
	}
	if ep, ok := e.(embedderProvider); ok && ep.Semantic() {
		return embedProviderOllama
	}
	return "hash"
}

// handleEmbeddingsCheckOllama reports whether Ollama is reachable and whether the
// bundled model is pulled.
func (h *DashboardHandler) handleEmbeddingsCheckOllama(w http.ResponseWriter, r *http.Request) {
	up := ollamaRunning(r.Context())
	writeJSONResp(w, http.StatusOK, map[string]any{
		"ollama_running":  up,
		"model":           embedModel,
		"model_available": up && ollamaHasModel(r.Context(), embedModel),
	})
}

// handleEmbeddingsPullModel streams `ollama pull nomic-embed-text` progress as
// text/plain lines ("status: msg", final "done: 0|1").
func (h *DashboardHandler) handleEmbeddingsPullModel(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	line := func(k, v string) { fmt.Fprintf(w, "%s: %s\n", k, v); flusher.Flush() }

	body, _ := json.Marshal(map[string]any{"name": embedModel, "stream": true})
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, ollamaBaseURL+"/api/pull", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		line("error", "cannot reach Ollama: "+err.Error())
		line("done", "1")
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var msg struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) == nil {
			if msg.Error != "" {
				line("error", msg.Error)
				line("done", "1")
				return
			}
			if msg.Status != "" {
				line("status", msg.Status)
			}
		}
	}
	line("done", "0")
}

// handleEmbeddingsReembed re-embeds every memory not yet on the Ollama vector
// space, streaming progress as text/plain "progress: done/total" lines. Requires
// the vault unlocked (it decrypts content + re-encrypts embeddings). Resumable:
// already-Ollama memories are skipped, so a re-run finishes an interrupted pass.
func (h *DashboardHandler) handleEmbeddingsReembed(w http.ResponseWriter, r *http.Request) {
	if h.VaultLocked.Load() {
		writeError(w, http.StatusForbidden, "unlock the vault before re-embedding")
		return
	}
	if !ollamaRunning(r.Context()) || !ollamaHasModel(r.Context(), embedModel) {
		writeError(w, http.StatusPreconditionFailed, "Ollama with "+embedModel+" is not available")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	line := func(k, v string) { fmt.Fprintf(w, "%s: %s\n", k, v); flusher.Flush() }

	// Total to do (memories not on Ollama).
	counts, _ := h.store.CountMemoriesByProvider(r.Context())
	total := 0
	for provider, n := range counts {
		if provider != embedProviderOllama {
			total += n
		}
	}
	line("total", fmt.Sprintf("%d", total))
	if total == 0 {
		line("done", "0")
		return
	}

	client := embedding.NewClient(ollamaBaseURL, embedModel)
	done, failed, offset := 0, 0, 0
	// Use the request context so a client disconnect / node shutdown stops the loop.
	ctx := r.Context()
	for {
		if ctx.Err() != nil {
			line("error", "cancelled")
			return
		}
		mems, err := h.store.ListMemoriesForReembed(ctx, reembedPageSize, offset)
		if err != nil {
			line("error", "list memories: "+err.Error())
			line("done", "1")
			return
		}
		if len(mems) == 0 {
			break
		}
		for _, m := range mems {
			// Skip memories already on the target embedder (resumable re-runs) or
			// with no content to embed. NOTE: this reads embedding_provider, NOT the
			// agent's `provider` column — those are distinct.
			if m.EmbeddingProvider == embedProviderOllama || strings.TrimSpace(m.Content) == "" {
				continue
			}
			emb, embErr := client.Embed(ctx, m.Content)
			if embErr != nil {
				failed++
				continue // skip; a later re-run retries it (still not tagged ollama)
			}
			if upErr := h.store.UpdateMemoryEmbedding(ctx, m.MemoryID, emb, embedProviderOllama); upErr != nil {
				failed++
				continue
			}
			done++
			if done%5 == 0 || done == total {
				line("progress", fmt.Sprintf("%d/%d", done, total))
			}
		}
		if len(mems) < reembedPageSize {
			break
		}
		offset += reembedPageSize
	}
	line("progress", fmt.Sprintf("%d/%d", done, total))
	if failed > 0 {
		line("warn", fmt.Sprintf("%d memories could not be re-embedded (will retry on the next run)", failed))
	}
	line("done", "0")
}

// handleEmbeddingsEnable switches the node's embedding provider to Ollama in
// config and restarts so every consumer (the /v1/embed endpoint, import, search)
// picks it up. Re-embedding should be run FIRST (while unlocked) so memories are
// already on the Ollama vector space when the switch takes effect.
func (h *DashboardHandler) handleEmbeddingsEnable(w http.ResponseWriter, r *http.Request) {
	if h.SetEmbeddingOllama == nil {
		writeError(w, http.StatusServiceUnavailable, "embedding switch not available on this node")
		return
	}
	if err := h.SetEmbeddingOllama(); err != nil {
		writeError(w, http.StatusInternalServerError, "enable ollama embeddings: "+err.Error())
		return
	}
	execPath, err := os.Executable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot determine binary path")
		return
	}
	writeJSONResp(w, http.StatusOK, map[string]any{"ok": true, "message": "Turning on semantic memory and restarting..."})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		if execErr := syscall.Exec(execPath, os.Args, os.Environ()); execErr != nil { //nolint:gosec // verified current binary
			_ = h.SetEmbeddingOllama // exec failed; process stays up in the old mode
		}
	}()
}

// ollamaRunning reports whether the local Ollama daemon answers /api/tags.
func ollamaRunning(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, ollamaBaseURL+"/api/tags", nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ollamaHasModel reports whether the given model is pulled locally.
func ollamaHasModel(ctx context.Context, model string) bool {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, ollamaBaseURL+"/api/tags", nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return false
	}
	for _, m := range tags.Models {
		// Ollama tags look like "nomic-embed-text:latest"; match the base name.
		if m.Name == model || strings.HasPrefix(m.Name, model+":") {
			return true
		}
	}
	return false
}
