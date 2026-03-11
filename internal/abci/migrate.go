package abci

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/l33tdawg/sage/internal/store"
	"github.com/l33tdawg/sage/internal/tx"
)

// MigrateAgentsOnChain reads all agents from the offchain store where on_chain_height = 0
// and broadcasts TxTypeAgentRegister transactions for each through CometBFT.
// This is a one-time startup migration — subsequent boots skip already-migrated agents.
func MigrateAgentsOnChain(ctx context.Context, agentStore store.AgentStore, badgerStore *store.BadgerStore, cometRPC string, signingKey ed25519.PrivateKey, logger zerolog.Logger) {
	if agentStore == nil || badgerStore == nil || cometRPC == "" {
		return
	}

	agents, err := agentStore.ListAgents(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("migrate: failed to list agents")
		return
	}

	// Backfill first_seen for any agents that have NULL first_seen
	for _, agent := range agents {
		if agent.FirstSeen == nil && !agent.CreatedAt.IsZero() {
			if updateErr := agentStore.BackfillFirstSeen(ctx, agent.AgentID, agent.CreatedAt); updateErr != nil {
				logger.Warn().Err(updateErr).Str("agent", agent.AgentID[:16]).Msg("migrate: failed to backfill first_seen")
			}
		}
	}

	migrated := 0
	skipped := 0
	for _, agent := range agents {
		if agent.Status == "removed" {
			continue
		}

		// If already registered on-chain but SQLite doesn't know, backfill
		if badgerStore.IsAgentRegistered(agent.AgentID) {
			if agent.OnChainHeight == 0 {
				if onChain, getErr := badgerStore.GetRegisteredAgent(agent.AgentID); getErr == nil && onChain != nil {
					agent.OnChainHeight = onChain.RegisteredAt
					if updateErr := agentStore.UpdateAgent(ctx, agent); updateErr != nil {
						logger.Warn().Err(updateErr).Str("agent", agent.AgentID[:16]).Msg("migrate: failed to backfill on_chain_height")
					} else {
						logger.Info().Str("agent", agent.AgentID[:16]).Int64("height", onChain.RegisteredAt).Msg("migrate: backfilled on_chain_height from BadgerDB")
					}
				}
			}
			skipped++
			continue
		}

		// Skip if already has on_chain_height set (migrated by a previous run
		// where ABCI processed it but this boot check runs again)
		if agent.OnChainHeight > 0 {
			skipped++
			continue
		}

		// Build and broadcast registration tx
		registerTx := &tx.ParsedTx{
			Type:      tx.TxTypeAgentRegister,
			Nonce:     uint64(time.Now().UnixNano()), // #nosec G115 -- nonce from timestamp
			Timestamp: time.Now(),
			AgentRegister: &tx.AgentRegister{
				AgentID:    agent.AgentID,
				Name:       agent.Name,
				Role:       agent.Role,
				BootBio:    agent.BootBio,
				Provider:   agent.Provider,
				P2PAddress: agent.P2PAddress,
			},
		}

		if signErr := tx.SignTx(registerTx, signingKey); signErr != nil {
			logger.Warn().Err(signErr).Str("agent", agent.AgentID[:16]).Msg("migrate: failed to sign tx")
			continue
		}

		encoded, encErr := tx.EncodeTx(registerTx)
		if encErr != nil {
			logger.Warn().Err(encErr).Str("agent", agent.AgentID[:16]).Msg("migrate: failed to encode tx")
			continue
		}

		txHex := hex.EncodeToString(encoded)
		url := fmt.Sprintf("%s/broadcast_tx_sync?tx=0x%s", cometRPC, txHex)

		resp, httpErr := httpGetWithTimeout(url)
		if httpErr != nil {
			logger.Warn().Err(httpErr).Str("agent", agent.AgentID[:16]).Msg("migrate: failed to broadcast tx")
			continue
		}
		resp.Body.Close()

		migrated++
		logger.Info().Str("agent", agent.AgentID[:16]).Str("name", agent.Name).Msg("migrate: agent registered on-chain")
	}

	if migrated > 0 || skipped > 0 {
		logger.Info().Int("migrated", migrated).Int("skipped", skipped).Int("total", len(agents)).Msg("agent on-chain migration complete")
	}
}

// httpGetWithTimeout does a GET request with a short timeout for migration broadcasts.
func httpGetWithTimeout(url string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
