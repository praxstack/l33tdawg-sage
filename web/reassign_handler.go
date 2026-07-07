package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/l33tdawg/sage/internal/governance"
	"github.com/l33tdawg/sage/internal/store"
	"github.com/l33tdawg/sage/internal/tx"
)

// This file implements the v11.3 RBAC DOMAIN-ownership transfer (the honest
// replacement for the retired authorship-rewrite "transfer tag" path). It moves
// a domain's on-chain OWNERSHIP + access from one agent to another using only
// existing txs (GovPropose -> DomainReassign -> AccessGrant); memory authorship
// (submitting_agent) is never touched. See RBAC-BUILD-SPEC.

// reassignStep records one on-chain step of the reassignment for an honest,
// per-step UI report.
type reassignStep struct {
	Name   string `json:"name"`
	TxHash string `json:"tx_hash,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// shortID truncates an agent id for human-readable messages.
func shortID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

// handleAgentDomains returns the distinct RBAC domains (domain_tag) an agent's
// memories live in, with an is_owner flag. This is the source list for the
// Search-page "transfer domain ownership" affordance. Note: this is DOMAINS
// (the RBAC unit), not memory_tags labels.
func (h *DashboardHandler) handleAgentDomains(agentStore store.AgentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		domains, err := agentStore.ListAgentDomains(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list agent domains: "+err.Error())
			return
		}
		type domainInfo struct {
			Domain  string `json:"domain"`
			IsOwner bool   `json:"is_owner"`
		}
		out := make([]domainInfo, 0, len(domains))
		for _, d := range domains {
			isOwner := false
			if h.BadgerStore != nil {
				if owner, oErr := h.BadgerStore.GetDomainOwner(d); oErr == nil && owner == id {
					isOwner = true
				}
			}
			out = append(out, domainInfo{Domain: d, IsOwner: isOwner})
		}
		writeJSONResp(w, http.StatusOK, map[string]any{"agent_id": id, "domains": out})
	}
}

// grantResult is one per-domain outcome of a matrix Save, for an honest UI.
type grantResult struct {
	Domain string `json:"domain"`
	Action string `json:"action"` // grant | revoke | skip
	Level  int    `json:"level,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// domainAccessEntry is one row of the read/write matrix blob.
type domainAccessEntry struct {
	Domain string `json:"domain"`
	Read   bool   `json:"read"`
	Write  bool   `json:"write"`
}

// parseDomainAccessLevels parses the matrix blob into domain -> desired grant
// level: write=3 (owner/modify), read-only=1 (access), neither=0 (no access).
// Empty blob => empty map (no domains configured). Malformed => nil (caller
// must not touch grants on a parse failure).
func parseDomainAccessLevels(blob string) map[string]int {
	if strings.TrimSpace(blob) == "" {
		return map[string]int{}
	}
	var entries []domainAccessEntry
	if err := json.Unmarshal([]byte(blob), &entries); err != nil {
		return nil
	}
	out := make(map[string]int, len(entries))
	for _, e := range entries {
		d := strings.TrimSpace(e.Domain)
		if d == "" {
			continue
		}
		switch {
		case e.Write:
			out[d] = 3
		case e.Read:
			out[d] = 1
		default:
			out[d] = 0
		}
	}
	return out
}

// reconcileDomainGrants issues real AccessGrant/AccessRevoke txs so the grantee
// agent's on-chain grants match the desired matrix state. It diffs the desired
// levels against the ACTUAL on-chain grant (GetAccessGrant), NOT the prior blob,
// so a grant that was previously skipped/deferred (owner key not local yet) or
// that failed self-heals on the next save, and the UI never reports a success
// that did not happen on-chain. Each grant/revoke is signed AS the domain OWNER
// (resolved locally) because that is who the consensus gate authorizes; domains
// whose owner key is not on this node are reported as skipped rather than
// silently dropped. This is the fix for the cosmetic-enforcement bug: the matrix
// now writes the enforced grant keys, not just the advisory blob. Consensus
// logic is untouched. oldBlob is used only to bound the candidate domain set.
func (h *DashboardHandler) reconcileDomainGrants(granteeID, oldBlob, newBlob string) []grantResult {
	if h.CometBFTRPC == "" || h.BadgerStore == nil || h.ResolveAgentKeyFn == nil {
		return nil
	}
	oldLevels := parseDomainAccessLevels(oldBlob)
	newLevels := parseDomainAccessLevels(newBlob)
	if newLevels == nil {
		return nil // malformed desired state - do not touch grants
	}
	domains := make(map[string]struct{}, len(oldLevels)+len(newLevels))
	for d := range newLevels {
		domains[d] = struct{}{}
	}
	for d := range oldLevels {
		domains[d] = struct{}{}
	}

	var results []grantResult
	for d := range domains {
		desired := newLevels[d] // 0 if absent from the new state
		// Read the current on-chain grant level for this grantee+domain (0 when
		// no grant exists) and act only on a real divergence.
		curLevel := 0
		if lvl, _, _, gErr := h.BadgerStore.GetAccessGrant(d, granteeID); gErr == nil {
			curLevel = int(lvl)
		}
		switch {
		case desired > 0 && curLevel != desired:
			results = append(results, h.grantAs(d, granteeID, desired))
		case desired == 0 && curLevel > 0:
			results = append(results, h.revokeAs(d, granteeID))
		default:
			// already in the desired on-chain state - no tx
		}
	}
	return results
}

// grantAs issues an AccessGrant(grantee, domain, level) signed as the domain
// owner. Skips (with a clear reason) when the domain is unowned or its owner
// key is not on this node.
func (h *DashboardHandler) grantAs(domain, granteeID string, level int) grantResult {
	owner, err := h.BadgerStore.GetDomainOwner(domain)
	if err != nil || owner == "" {
		return grantResult{Domain: domain, Action: "skip", Level: level, OK: false,
			Error: "domain has no on-chain owner yet, so access cannot be granted (submit a memory to it or reassign it first)"}
	}
	ownerKey, ok := h.ResolveAgentKeyFn(owner)
	if !ok {
		return grantResult{Domain: domain, Action: "skip", Level: level, OK: false,
			Error: fmt.Sprintf("domain is owned by %s, whose signing key is not on this node", shortID(owner))}
	}
	grantTx := &tx.ParsedTx{
		Type: tx.TxTypeAccessGrant,
		AccessGrant: &tx.AccessGrant{
			GranterID: owner,
			GranteeID: granteeID,
			Domain:    domain,
			Level:     uint8(level), // #nosec G115 -- level is 1 or 3
		},
	}
	if _, _, _, gErr := h.signAndBroadcastCommit(grantTx, ownerKey); gErr != nil {
		return grantResult{Domain: domain, Action: "grant", Level: level, OK: false, Error: gErr.Error()}
	}
	return grantResult{Domain: domain, Action: "grant", Level: level, OK: true}
}

// revokeAs issues an AccessRevoke(grantee, domain) signed as the domain owner.
func (h *DashboardHandler) revokeAs(domain, granteeID string) grantResult {
	owner, err := h.BadgerStore.GetDomainOwner(domain)
	if err != nil || owner == "" {
		return grantResult{Domain: domain, Action: "skip", OK: false,
			Error: "domain has no on-chain owner, so there is nothing to revoke"}
	}
	if owner == granteeID {
		// An owner cannot be meaningfully revoked from its own domain (it keeps
		// ownership regardless); revoking here would only delete its direct-grant
		// fast path. Use domain reassignment to move ownership instead.
		return grantResult{Domain: domain, Action: "skip", OK: false,
			Error: "agent owns this domain; access is not revoked from the matrix (use domain reassignment to transfer ownership)"}
	}
	ownerKey, ok := h.ResolveAgentKeyFn(owner)
	if !ok {
		return grantResult{Domain: domain, Action: "skip", OK: false,
			Error: fmt.Sprintf("domain is owned by %s, whose signing key is not on this node", shortID(owner))}
	}
	revokeTx := &tx.ParsedTx{
		Type: tx.TxTypeAccessRevoke,
		AccessRevoke: &tx.AccessRevoke{
			RevokerID: owner,
			GranteeID: granteeID,
			Domain:    domain,
			Reason:    "dashboard access matrix update",
		},
	}
	if _, _, _, rErr := h.signAndBroadcastCommit(revokeTx, ownerKey); rErr != nil {
		return grantResult{Domain: domain, Action: "revoke", OK: false, Error: rErr.Error()}
	}
	return grantResult{Domain: domain, Action: "revoke", OK: true}
}

// mirrorDomainAccessSet updates an agent's off-chain DomainAccess blob so the
// Agents access-matrix reflects an on-chain grant change made elsewhere (e.g. a
// domain reassignment). present=false removes the domain from the blob;
// present=true sets it to read+write. Best-effort mirror maintenance only - the
// on-chain grant keys remain authoritative.
func (h *DashboardHandler) mirrorDomainAccessSet(ctx context.Context, agentStore store.AgentStore, agentID, domain string, present bool) {
	ag, err := agentStore.GetAgent(ctx, agentID)
	if err != nil || ag == nil {
		return
	}
	var entries []domainAccessEntry
	if strings.TrimSpace(ag.DomainAccess) != "" {
		if jErr := json.Unmarshal([]byte(ag.DomainAccess), &entries); jErr != nil {
			return
		}
	}
	out := make([]domainAccessEntry, 0, len(entries)+1)
	found := false
	for _, e := range entries {
		if e.Domain == domain {
			found = true
			if present {
				e.Read, e.Write = true, true
				out = append(out, e)
			}
			continue
		}
		out = append(out, e)
	}
	if present && !found {
		out = append(out, domainAccessEntry{Domain: domain, Read: true, Write: true})
	}
	blob, mErr := json.Marshal(out)
	if mErr != nil {
		return
	}
	ag.DomainAccess = string(blob)
	_ = agentStore.UpdateAgent(ctx, ag)
}

// cancelActiveProposal best-effort cancels the just-created governance proposal
// (signed as the admin proposer, the only party engine.Cancel allows) so a
// failure after a successful propose does not leave gov:active set - which would
// block a retry AND all other governance until the proposal expires. Ignores
// errors: if the proposal already executed and cleared gov:active, there is
// nothing to cancel.
func (h *DashboardHandler) cancelActiveProposal(proposalID string) {
	if h.AdminSigningKey == nil {
		return
	}
	cancelTx := &tx.ParsedTx{
		Type:      tx.TxTypeGovCancel,
		GovCancel: &tx.GovCancel{ProposalID: proposalID},
	}
	_, _, _, _ = h.signAndBroadcastCommit(cancelTx, h.AdminSigningKey)
}

// handleReassignDomainOwnership performs the RBAC domain-ownership transfer
// A->B on-chain, in strict commit-confirmed order:
//
//  1. GovPropose(domain_reassign)         signed as the operator/admin key.
//     On a single validator this self-passes to Executed in the same block.
//  2. DomainReassign(domain -> B)         signed as admin; flips owner and
//     purges ALL grants on the domain.
//  3. AccessGrant(B, level 3)             signed AS B (the new owner) so B can
//     read+write; deferred if B's key is not on this node.
//
// Memory authorship (submitting_agent) is NEVER rewritten. Each step is
// commit-confirmed so consensus rejections surface honestly.
func (h *DashboardHandler) handleReassignDomainOwnership(agentStore store.AgentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.CometBFTRPC == "" {
			writeError(w, http.StatusServiceUnavailable, "CometBFT consensus not configured")
			return
		}
		if h.AdminSigningKey == nil {
			writeError(w, http.StatusServiceUnavailable, "admin signing key not available (operator key ~/.sage/agent.key missing), so a domain reassignment cannot be authorized")
			return
		}
		if h.SigningKey == nil {
			writeError(w, http.StatusServiceUnavailable, "validator signing key not available, so the reassignment proposal cannot be voted through")
			return
		}
		// The flow drives the governance proposal to Executed in-band by casting
		// the sole validator's accept vote. That only reaches quorum on a
		// single-validator node; a multi-validator chain needs the other
		// validators to vote, which this endpoint does not orchestrate.
		if h.ValidatorCountFn != nil && h.ValidatorCountFn() > 1 {
			writeError(w, http.StatusConflict, "domain reassignment from the dashboard requires a single-validator node; this chain has multiple validators that must vote on the proposal")
			return
		}

		var req struct {
			SourceAgentID string `json:"source_agent_id"`
			TargetAgentID string `json:"target_agent_id"`
			Domain        string `json:"domain"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req.SourceAgentID = strings.TrimSpace(req.SourceAgentID)
		req.TargetAgentID = strings.TrimSpace(req.TargetAgentID)
		req.Domain = strings.TrimSpace(req.Domain)
		if req.TargetAgentID == "" || req.Domain == "" {
			writeError(w, http.StatusBadRequest, "target_agent_id and domain are required")
			return
		}
		if req.SourceAgentID == req.TargetAgentID {
			writeError(w, http.StatusBadRequest, "source and target agent cannot be the same")
			return
		}
		// The new owner must be a registered agent.
		if _, err := agentStore.GetAgent(r.Context(), req.TargetAgentID); err != nil {
			writeError(w, http.StatusBadRequest, "target agent not found in registry")
			return
		}
		// The domain must exist on-chain (else there is nothing to reassign).
		if h.BadgerStore != nil {
			if _, err := h.BadgerStore.GetDomainOwner(req.Domain); err != nil {
				writeError(w, http.StatusBadRequest, "domain not found on-chain: "+req.Domain)
				return
			}
		}

		var steps []reassignStep
		fail := func(status int, name, msg string) {
			steps = append(steps, reassignStep{Name: name, OK: false, Error: msg})
			full := fmt.Sprintf("%s step failed: %s", name, msg)
			// Include both "error" and "message": the frontend reads e.error on a
			// non-2xx response, and this endpoint's honest step diagnostic must
			// reach the operator rather than collapsing to the HTTP status text.
			writeJSONResp(w, status, map[string]any{
				"status":  "error",
				"steps":   steps,
				"domain":  req.Domain,
				"error":   full,
				"message": full,
			})
		}

		adminKey := h.AdminSigningKey
		adminID := agentIDForKey(adminKey)

		// Step 1: propose. Payload is the DomainReassign body the executing tx
		// must reproduce byte-for-byte (parity check).
		payload, mErr := json.Marshal(tx.DomainReassign{
			Domain:       req.Domain,
			NewOwnerID:   req.TargetAgentID,
			ParentDomain: "",
			OpenToShared: false,
		})
		if mErr != nil {
			fail(http.StatusInternalServerError, "propose", "encode payload: "+mErr.Error())
			return
		}
		proposeTx := &tx.ParsedTx{
			Type: tx.TxTypeGovPropose,
			GovPropose: &tx.GovPropose{
				Operation: tx.GovOpDomainReassign,
				TargetID:  req.Domain,
				Reason:    fmt.Sprintf("dashboard: reassign domain %q to %s", req.Domain, shortID(req.TargetAgentID)),
				Payload:   payload,
			},
		}
		proposeHash, height, _, pErr := h.signAndBroadcastCommit(proposeTx, adminKey)
		if pErr != nil {
			fail(http.StatusBadGateway, "propose", pErr.Error())
			return
		}
		steps = append(steps, reassignStep{Name: "propose", TxHash: proposeHash, OK: true})

		// Step 2: the executing tx references the proposal by its DETERMINISTIC
		// id (proposerID = admin key's agent id, height = the propose block),
		// NOT the propose tx hash.
		proposalID := governance.ComputeProposalID(adminID, height, governance.OpDomainReassign, req.Domain)

		// Step 3: cast the validator's accept vote. The admin proposer auto-votes
		// at propose time, but the admin key is NOT in the validator set, so that
		// vote is not counted toward quorum (CheckGovQuorumOp tallies only
		// validator votes). The sole validator (h.SigningKey) must vote accept for
		// the proposal to pass and self-execute; without this the DomainReassign
		// below fails Code 82 (proposal not executed). The vote lands in a later
		// block than the commit-confirmed propose, so the proposal is active when
		// it is cast and Executed once the vote block commits. Skip when the admin
		// proposer IS the validator (its auto-vote already counts; a second vote
		// would be rejected as a duplicate).
		if adminID != agentIDForKey(h.SigningKey) {
			voteTx := &tx.ParsedTx{
				Type: tx.TxTypeGovVote,
				GovVote: &tx.GovVote{
					ProposalID: proposalID,
					Decision:   tx.VoteDecisionAccept,
				},
			}
			voteHash, _, _, vErr := h.signAndBroadcastCommit(voteTx, h.SigningKey)
			if vErr != nil {
				// Clear the dangling active proposal so a retry and other
				// governance are not blocked until it expires.
				h.cancelActiveProposal(proposalID)
				fail(http.StatusBadGateway, "vote", vErr.Error())
				return
			}
			steps = append(steps, reassignStep{Name: "vote", TxHash: voteHash, OK: true})
		}

		// Step 4: execute the reassignment (admin-signed). Flips owner -> B and
		// purges every grant on the domain.
		reassignTx := &tx.ParsedTx{
			Type: tx.TxTypeDomainReassign,
			DomainReassign: &tx.DomainReassign{
				Domain:       req.Domain,
				NewOwnerID:   req.TargetAgentID,
				ParentDomain: "",
				ProposalID:   proposalID,
				OpenToShared: false,
			},
		}
		reassignHash, _, reassignLog, rErr := h.signAndBroadcastCommit(reassignTx, adminKey)
		if rErr != nil {
			// If the proposal already executed, this is a no-op; otherwise it
			// clears the dangling active proposal.
			h.cancelActiveProposal(proposalID)
			fail(http.StatusBadGateway, "reassign", rErr.Error())
			return
		}
		steps = append(steps, reassignStep{Name: "reassign", TxHash: reassignHash, OK: true})
		purged := parsePurgedGrantsWeb(reassignLog)

		// Step 5: grant the new owner explicit level-3 access (ownership alone
		// does not imply access; the reassign purged all grants). Must be signed
		// AS B. If B's key is not local, defer to B's own node.
		grantDeferred := false
		grantMsg := ""
		if h.ResolveAgentKeyFn != nil {
			if ownerKey, ok := h.ResolveAgentKeyFn(req.TargetAgentID); ok {
				grantTx := &tx.ParsedTx{
					Type: tx.TxTypeAccessGrant,
					AccessGrant: &tx.AccessGrant{
						GranterID: req.TargetAgentID,
						GranteeID: req.TargetAgentID,
						Domain:    req.Domain,
						Level:     3,
					},
				}
				grantHash, _, _, gErr := h.signAndBroadcastCommit(grantTx, ownerKey)
				if gErr != nil {
					steps = append(steps, reassignStep{Name: "grant", OK: false, Error: gErr.Error()})
				} else {
					steps = append(steps, reassignStep{Name: "grant", TxHash: grantHash, OK: true})
				}
			} else {
				grantDeferred = true
				grantMsg = "the new owner's signing key is not on this node, so the owner must grant itself domain access from its own node"
				steps = append(steps, reassignStep{Name: "grant", OK: false, Error: "deferred: " + grantMsg})
			}
		} else {
			grantDeferred = true
			grantMsg = "no local key resolver available, so the owner must grant itself domain access"
			steps = append(steps, reassignStep{Name: "grant", OK: false, Error: "deferred: " + grantMsg})
		}

		// Keep the off-chain access-matrix mirror consistent with the on-chain
		// transfer: drop the domain from the source agent's DomainAccess blob and
		// add it (read+write) to the new owner's, so the Agents matrix does not
		// show a stale grant a later save would try to re-issue. Best-effort;
		// on-chain state (above) is authoritative.
		if req.SourceAgentID != "" {
			h.mirrorDomainAccessSet(r.Context(), agentStore, req.SourceAgentID, req.Domain, false)
		}
		h.mirrorDomainAccessSet(r.Context(), agentStore, req.TargetAgentID, req.Domain, true)

		status := "ok"
		for _, s := range steps {
			if !s.OK {
				status = "partial"
				break
			}
		}
		msg := fmt.Sprintf("Domain %q ownership transferred to %s. %d prior grants purged, so the source agent's access grant on this domain is revoked. Authorship is unchanged.", req.Domain, shortID(req.TargetAgentID), purged)
		if grantMsg != "" {
			msg = msg + " Note: " + grantMsg + "."
		}

		if h.SSE != nil {
			h.SSE.Broadcast(SSEEvent{
				Type:    EventGovernance,
				Content: fmt.Sprintf("Domain %q reassigned to %s", req.Domain, shortID(req.TargetAgentID)),
				Data: map[string]any{
					"action": "domain_reassign",
					"domain": req.Domain,
					"target": req.TargetAgentID,
				},
			})
		}

		writeJSONResp(w, http.StatusOK, map[string]any{
			"status":         status,
			"steps":          steps,
			"purged_grants":  purged,
			"grant_deferred": grantDeferred,
			"source":         req.SourceAgentID,
			"target":         req.TargetAgentID,
			"domain":         req.Domain,
			"message":        msg,
		})
	}
}
