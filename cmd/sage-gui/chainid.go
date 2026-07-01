package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// chainIDSuffixLen is the number of lowercase base32 characters of hash material
// appended after the human prefix. 26 chars ≈ 130 bits of the digest — birthday-
// safe for any realistic population of SAGE networks — and keeps the total id
// well under CometBFT's MaxChainIDLen (50):
//
//	len("sage-personal") + 1 + 26 = 40.
//
// The full base32 of a sha256 digest is 52 chars, which alone exceeds
// MaxChainIDLen, so truncation here is load-bearing, not cosmetic.
const chainIDSuffixLen = 26

// maxChainIDLen mirrors CometBFT's types.MaxChainIDLen (50). genDoc.ValidateAndComplete
// enforces it downstream; we also check at the source so mintChainID fails loudly
// rather than deferring the error to genesis validation.
const maxChainIDLen = 50

// mintChainID derives a globally-unique chain_id from genesis material.
//
// Shape: <prefix>-<lowercase base32(sha256(sortedValidatorPubkeys ‖ genesisTimeUnixNanoBE ‖ 16 random bytes))[:chainIDSuffixLen]>
//
// The digest binds the genesis validator set and genesis time (so two networks
// with different founders/times differ even absent the salt) plus 16 crypto/rand
// bytes (so re-initialising the SAME validator set still yields a fresh id). The
// result is a stable, opaque, network-unique identifier safe to use as the
// CometBFT genesis ChainID (and thus the NodeInfo.Network p2p tag) and as the
// SAGE federation identity. The character class (lowercase letters, digits 2-7,
// and the prefix hyphen) is a strict subset of what the legacy "sage-personal"
// id already used, so it passes every CometBFT genesis/p2p validation the old
// literal did — only length was ever the risk, and that is bounded here.
func mintChainID(prefix string, valPubkeys [][]byte, genesisTime time.Time) (string, error) {
	h := sha256.New()

	// 1. validator pubkeys, sorted for order-independence across founders.
	sorted := make([][]byte, len(valPubkeys))
	copy(sorted, valPubkeys)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	for _, pk := range sorted {
		h.Write(pk)
	}

	// 2. genesis time as big-endian UnixNano.
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(genesisTime.UnixNano()))
	h.Write(t[:])

	// 3. 16 bytes of entropy so a re-init of the same validator set is still unique.
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	h.Write(salt[:])

	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	suffix := strings.ToLower(enc.EncodeToString(h.Sum(nil)))
	if len(suffix) > chainIDSuffixLen {
		suffix = suffix[:chainIDSuffixLen]
	}

	id := prefix + "-" + suffix
	if len(id) > maxChainIDLen {
		// Only reachable with an over-long prefix; suffix is fixed-width.
		return "", fmt.Errorf("minted chain_id %q exceeds max length %d", id, maxChainIDLen)
	}
	return id, nil
}

// readChainIDFromGenesis returns the ChainID recorded in a node's
// config/genesis.json — the authoritative origin of a chain's identity.
// CometBFT persists the id into state.db after first boot and reads it from
// there forever after, so genesis.json is the single durable source of truth;
// editing it post-boot is silently ignored. We reconcile cfg.ChainID from this
// on every serve so the id is surfaced read-only and available before CometBFT
// is up, and so existing (grandfathered) chains backfill their id without any
// destructive re-genesis. Reads only chain_id (see below) so it needs no
// ValidateAndComplete and tolerates any genesis a running node produced.
func readChainIDFromGenesis(cometHome string) (string, error) {
	genesisPath := filepath.Join(cometHome, "config", "genesis.json")
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		return "", err
	}
	// Unmarshal ONLY chain_id into a minimal struct. A full cmttypes.GenesisDoc
	// unmarshal via encoding/json trips over CometBFT's string-encoded int64
	// fields (e.g. "initial_height": "0", written by cmtjson in genDoc.SaveAs);
	// we need only the id, so ignore every other field.
	var gen struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.Unmarshal(data, &gen); err != nil {
		return "", fmt.Errorf("parse genesis: %w", err)
	}
	return gen.ChainID, nil
}
