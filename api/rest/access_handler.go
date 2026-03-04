package rest

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/l33tdawg/sage/api/rest/middleware"
	"github.com/l33tdawg/sage/internal/tx"
)

// --- Request types -----------------------------------------------------------

// AccessRequestReq is the JSON body for POST /v1/access/request.
type AccessRequestReq struct {
	TargetDomain   string `json:"target_domain"`
	Justification  string `json:"justification,omitempty"`
	RequestedLevel int    `json:"requested_level,omitempty"`
}

// AccessGrantReq is the JSON body for POST /v1/access/grant.
type AccessGrantReq struct {
	GranteeID string `json:"grantee_id"`
	Domain    string `json:"domain"`
	Level     int    `json:"level,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// AccessRevokeReq is the JSON body for POST /v1/access/revoke.
type AccessRevokeReq struct {
	GranteeID string `json:"grantee_id"`
	Domain    string `json:"domain"`
	Reason    string `json:"reason,omitempty"`
}

// DomainRegisterReq is the JSON body for POST /v1/domain/register.
type DomainRegisterReq struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parent      string `json:"parent,omitempty"`
}

// --- Handlers ----------------------------------------------------------------

// handleAccessRequest handles POST /v1/access/request.
func (s *Server) handleAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req AccessRequestReq
	err := decodeJSON(r, &req)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	if req.TargetDomain == "" {
		writeProblem(w, http.StatusBadRequest, "Missing target domain", "target_domain is required")
		return
	}
	if req.RequestedLevel == 0 {
		req.RequestedLevel = 1
	}

	agentID := middleware.ContextAgentID(r.Context())

	accessTx := &tx.ParsedTx{
		Type:      tx.TxTypeAccessRequest,
		Nonce:     uint64(time.Now().UnixNano()), // #nosec G115 -- nonce from timestamp
		Timestamp: time.Now(),
		AccessRequest: &tx.AccessRequest{
			RequesterID:    agentID,
			TargetDomain:   req.TargetDomain,
			Justification:  req.Justification,
			RequestedLevel: uint8(req.RequestedLevel), // #nosec G115 -- validated small int
		},
	}

	embedAgentAuth(r.Context(), accessTx)

	err = tx.SignTx(accessTx, s.signingKey)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to sign access request tx")
		writeProblem(w, http.StatusInternalServerError, "Signing error", "Failed to sign transaction.")
		return
	}

	encoded, err := tx.EncodeTx(accessTx)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to encode access request tx")
		writeProblem(w, http.StatusInternalServerError, "Encoding error", "Failed to encode transaction.")
		return
	}

	txHash, err := s.broadcastTx(encoded)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to broadcast access request tx")
		writeProblem(w, http.StatusInternalServerError, "Broadcast error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status":  "pending",
		"tx_hash": txHash,
	})
}

// handleAccessGrant handles POST /v1/access/grant.
func (s *Server) handleAccessGrant(w http.ResponseWriter, r *http.Request) {
	var req AccessGrantReq
	err := decodeJSON(r, &req)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	if req.GranteeID == "" {
		writeProblem(w, http.StatusBadRequest, "Missing grantee ID", "grantee_id is required")
		return
	}
	if req.Domain == "" {
		writeProblem(w, http.StatusBadRequest, "Missing domain", "domain is required")
		return
	}
	if req.Level == 0 {
		req.Level = 1
	}

	agentID := middleware.ContextAgentID(r.Context())

	grantTx := &tx.ParsedTx{
		Type:      tx.TxTypeAccessGrant,
		Nonce:     uint64(time.Now().UnixNano()), // #nosec G115 -- nonce from timestamp
		Timestamp: time.Now(),
		AccessGrant: &tx.AccessGrant{
			GranterID: agentID,
			GranteeID: req.GranteeID,
			Domain:    req.Domain,
			Level:     uint8(req.Level), // #nosec G115 -- validated small int
			ExpiresAt: req.ExpiresAt,
			RequestID: req.RequestID,
		},
	}

	embedAgentAuth(r.Context(), grantTx)

	err = tx.SignTx(grantTx, s.signingKey)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to sign access grant tx")
		writeProblem(w, http.StatusInternalServerError, "Signing error", "Failed to sign transaction.")
		return
	}

	encoded, err := tx.EncodeTx(grantTx)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to encode access grant tx")
		writeProblem(w, http.StatusInternalServerError, "Encoding error", "Failed to encode transaction.")
		return
	}

	txHash, err := s.broadcastTx(encoded)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to broadcast access grant tx")
		writeProblem(w, http.StatusInternalServerError, "Broadcast error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status":  "granted",
		"tx_hash": txHash,
	})
}

// handleAccessRevoke handles POST /v1/access/revoke.
func (s *Server) handleAccessRevoke(w http.ResponseWriter, r *http.Request) {
	var req AccessRevokeReq
	err := decodeJSON(r, &req)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	if req.GranteeID == "" {
		writeProblem(w, http.StatusBadRequest, "Missing grantee ID", "grantee_id is required")
		return
	}
	if req.Domain == "" {
		writeProblem(w, http.StatusBadRequest, "Missing domain", "domain is required")
		return
	}

	agentID := middleware.ContextAgentID(r.Context())

	revokeTx := &tx.ParsedTx{
		Type:      tx.TxTypeAccessRevoke,
		Nonce:     uint64(time.Now().UnixNano()), // #nosec G115 -- nonce from timestamp
		Timestamp: time.Now(),
		AccessRevoke: &tx.AccessRevoke{
			RevokerID: agentID,
			GranteeID: req.GranteeID,
			Domain:    req.Domain,
			Reason:    req.Reason,
		},
	}

	embedAgentAuth(r.Context(), revokeTx)

	err = tx.SignTx(revokeTx, s.signingKey)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to sign access revoke tx")
		writeProblem(w, http.StatusInternalServerError, "Signing error", "Failed to sign transaction.")
		return
	}

	encoded, err := tx.EncodeTx(revokeTx)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to encode access revoke tx")
		writeProblem(w, http.StatusInternalServerError, "Encoding error", "Failed to encode transaction.")
		return
	}

	txHash, err := s.broadcastTx(encoded)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to broadcast access revoke tx")
		writeProblem(w, http.StatusInternalServerError, "Broadcast error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "revoked",
		"tx_hash": txHash,
	})
}

// handleListGrants handles GET /v1/access/grants/{agent_id}.
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agent_id")
	if agentID == "" {
		agentID = middleware.ContextAgentID(r.Context())
	}

	if s.accessStore == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Access store unavailable",
			"Access control storage is not configured.")
		return
	}

	grants, err := s.accessStore.GetActiveGrants(r.Context(), agentID)
	if err != nil {
		s.logger.Error().Err(err).Str("agent_id", agentID).Msg("failed to get grants")
		writeProblem(w, http.StatusInternalServerError, "Query error", "Failed to query access grants.")
		return
	}

	writeJSON(w, http.StatusOK, grants)
}

// handleDomainRegister handles POST /v1/domain/register.
func (s *Server) handleDomainRegister(w http.ResponseWriter, r *http.Request) {
	var req DomainRegisterReq
	err := decodeJSON(r, &req)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "Missing domain name", "name is required")
		return
	}

	agentID := middleware.ContextAgentID(r.Context())

	domainTx := &tx.ParsedTx{
		Type:      tx.TxTypeDomainRegister,
		Nonce:     uint64(time.Now().UnixNano()), // #nosec G115 -- nonce from timestamp
		Timestamp: time.Now(),
		DomainRegister: &tx.DomainRegister{
			DomainName:   req.Name,
			OwnerAgentID: agentID,
			Description:  req.Description,
			ParentDomain: req.Parent,
		},
	}

	embedAgentAuth(r.Context(), domainTx)

	err = tx.SignTx(domainTx, s.signingKey)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to sign domain register tx")
		writeProblem(w, http.StatusInternalServerError, "Signing error", "Failed to sign transaction.")
		return
	}

	encoded, err := tx.EncodeTx(domainTx)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to encode domain register tx")
		writeProblem(w, http.StatusInternalServerError, "Encoding error", "Failed to encode transaction.")
		return
	}

	txHash, err := s.broadcastTx(encoded)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to broadcast domain register tx")
		writeProblem(w, http.StatusInternalServerError, "Broadcast error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status":  "registered",
		"tx_hash": txHash,
	})
}

// handleGetDomain handles GET /v1/domain/{name}.
func (s *Server) handleGetDomain(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeProblem(w, http.StatusBadRequest, "Missing domain name", "name path parameter is required")
		return
	}

	if s.accessStore == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Access store unavailable",
			"Access control storage is not configured.")
		return
	}

	domain, err := s.accessStore.GetDomain(r.Context(), name)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Domain not found",
			fmt.Sprintf("No domain found with name %s", name))
		return
	}

	writeJSON(w, http.StatusOK, domain)
}
