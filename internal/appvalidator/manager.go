package appvalidator

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"

	"github.com/rs/zerolog"
)

// Manager orchestrates all 4 validators and manages their signing keys.
type Manager struct {
	validators []Validator
	keys       []ed25519.PrivateKey
	agentIDs   []string
	logger     zerolog.Logger
}

var validatorNames = []string{"sentinel", "dedup", "quality", "consistency"}

// NewManager creates a Manager with 4 deterministic validator keys derived from seed.
func NewManager(seed []byte, hashChecker ContentHashChecker, logger zerolog.Logger) *Manager {
	m := &Manager{
		logger: logger,
	}

	seedHex := hex.EncodeToString(seed)

	for _, name := range validatorNames {
		// Derive deterministic key: sha256("sage-validator-{name}-" + hex(seed))
		material := sha256.Sum256([]byte("sage-validator-" + name + "-" + seedHex))
		privKey := ed25519.NewKeyFromSeed(material[:])
		pubKey := privKey.Public().(ed25519.PublicKey)

		// Agent ID = hex(sha256(pubkey))
		idHash := sha256.Sum256(pubKey)
		agentID := hex.EncodeToString(idHash[:])

		m.keys = append(m.keys, privKey)
		m.agentIDs = append(m.agentIDs, agentID)
	}

	// Create the 4 validators
	m.validators = []Validator{
		&SentinelValidator{},
		NewDedupValidator(hashChecker),
		&QualityValidator{},
		&ConsistencyValidator{},
	}

	logger.Info().
		Int("count", len(m.validators)).
		Strs("ids", m.agentIDs).
		Msg("app validators initialized")

	return m
}

// PreValidate runs all 4 validators and returns the consensus result.
// accepted is true if >= 3 out of 4 accept (BFT quorum: 3/4 >= 2/3).
func (m *Manager) PreValidate(content, contentHash, domain, memType string, confidence float64) (accepted bool, results []VoteResult) {
	results = make([]VoteResult, 0, len(m.validators))
	acceptCount := 0

	for _, v := range m.validators {
		result := v.Validate(content, contentHash, domain, memType, confidence)
		results = append(results, result)

		if result.Decision == "accept" {
			acceptCount++
		}

		m.logger.Debug().
			Str("validator", result.ValidatorName).
			Str("decision", result.Decision).
			Str("reason", result.Reason).
			Msg("validator vote")
	}

	accepted = acceptCount >= 3

	m.logger.Info().
		Int("accept", acceptCount).
		Int("total", len(m.validators)).
		Bool("accepted", accepted).
		Msg("pre-validation complete")

	return accepted, results
}

// GetValidatorIDs returns the 4 agent IDs for ABCI registration.
func (m *Manager) GetValidatorIDs() []string {
	ids := make([]string, len(m.agentIDs))
	copy(ids, m.agentIDs)
	return ids
}

// GetValidatorKeys returns the 4 private keys for signing vote TXs.
func (m *Manager) GetValidatorKeys() []ed25519.PrivateKey {
	keys := make([]ed25519.PrivateKey, len(m.keys))
	copy(keys, m.keys)
	return keys
}
