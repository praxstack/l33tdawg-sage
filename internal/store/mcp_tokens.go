package store

// Bearer-token storage for the HTTP MCP transport.
//
// External MCP clients (ChatGPT, Cursor, Cline, custom HTTP clients) cannot
// easily ed25519-sign every request the way the SAGE REST API expects, so
// we expose a long-lived bearer-token path for them.
//
// Storage model:
//   - Tokens are 32 random bytes, base64-url-encoded — they're shown to the
//     operator exactly once at create-time and never readable again.
//   - We store the SHA-256 digest of the token, never the token itself, so
//     a database compromise cannot leak working credentials.
//   - On every MCP request, the bearer token is hashed and looked up by
//     digest.
//
// This file lives alongside the other SQLiteStore methods so it inherits
// the same writeMu / encryption / vault patterns.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MCPToken describes one issued bearer token. The actual token value is
// only ever returned once — see IssueMCPToken.
type MCPToken struct {
	ID         string    // stable identifier (UUID)
	Name       string    // human label set at create-time (e.g. "chatgpt-laptop")
	AgentID    string    // the on-chain agent identity this token authenticates as
	CreatedAt  time.Time // issuance time
	LastUsedAt time.Time // updated on every successful auth (zero if never used)
	RevokedAt  time.Time // zero unless revoked
}

// ErrTokenRevoked is returned by LookupMCPToken when a token exists but has
// been explicitly revoked. Distinct from "no row" so callers can log it.
var ErrTokenRevoked = errors.New("mcp token revoked")

// migrateMCPTokens creates the mcp_tokens table on first boot. Idempotent.
// We store SHA-256(token), never the token itself.
func (s *SQLiteStore) migrateMCPTokens(ctx context.Context) {
	_, _ = s.writeExecContext(ctx, `
	CREATE TABLE IF NOT EXISTS mcp_tokens (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL DEFAULT '',
		agent_id      TEXT NOT NULL,
		token_sha256  TEXT NOT NULL UNIQUE,
		created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		last_used_at  TEXT,
		revoked_at    TEXT
	)`)
	_, _ = s.writeExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_mcp_tokens_agent ON mcp_tokens(agent_id)`)
	_, _ = s.writeExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_mcp_tokens_active ON mcp_tokens(token_sha256) WHERE revoked_at IS NULL`)
}

// InsertMCPToken records a newly-issued token by its SHA-256 digest. The
// token's plaintext value is never persisted. Caller hands the plaintext
// back to the user once and never touches it again.
func (s *SQLiteStore) InsertMCPToken(ctx context.Context, id, name, agentID, tokenSHA256 string) error {
	if id == "" || agentID == "" || tokenSHA256 == "" {
		return fmt.Errorf("id, agent_id, and tokenSHA256 are required")
	}
	_, err := s.writeExecContext(ctx,
		`INSERT INTO mcp_tokens (id, name, agent_id, token_sha256) VALUES (?, ?, ?, ?)`,
		id, name, agentID, tokenSHA256)
	if err != nil {
		return fmt.Errorf("insert mcp token: %w", err)
	}
	return nil
}

// LookupMCPToken finds an active (non-revoked) token by its SHA-256 digest
// and returns the associated agent ID. Side-effect: bumps last_used_at to
// now() so the operator can see when each token was last hit.
//
// Returns sql.ErrNoRows if no token matches and ErrTokenRevoked if the
// token exists but was revoked.
func (s *SQLiteStore) LookupMCPToken(ctx context.Context, tokenSHA256 string) (*MCPToken, error) {
	row := s.conn.QueryRowContext(ctx, `
		SELECT id, name, agent_id, created_at, COALESCE(last_used_at, ''), COALESCE(revoked_at, '')
		  FROM mcp_tokens
		 WHERE token_sha256 = ?`, tokenSHA256)

	var tok MCPToken
	var createdAt, lastUsedAt, revokedAt string
	if err := row.Scan(&tok.ID, &tok.Name, &tok.AgentID, &createdAt, &lastUsedAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("lookup mcp token: %w", err)
	}

	tok.CreatedAt = parseTime(createdAt)
	if lastUsedAt != "" {
		tok.LastUsedAt = parseTime(lastUsedAt)
	}
	if revokedAt != "" {
		tok.RevokedAt = parseTime(revokedAt)
		return &tok, ErrTokenRevoked
	}

	// Best-effort last_used_at update — never fail auth on a write error.
	_, _ = s.writeExecContext(ctx,
		`UPDATE mcp_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		tok.ID)

	return &tok, nil
}

// ListMCPTokens returns all issued tokens (active + revoked) so the
// dashboard / CLI can show issuance history. Token values are NEVER
// returned (we don't have them — only digests).
func (s *SQLiteStore) ListMCPTokens(ctx context.Context) ([]*MCPToken, error) {
	rows, err := s.conn.QueryContext(ctx, `
		SELECT id, name, agent_id, created_at, COALESCE(last_used_at, ''), COALESCE(revoked_at, '')
		  FROM mcp_tokens
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list mcp tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*MCPToken
	for rows.Next() {
		var tok MCPToken
		var createdAt, lastUsedAt, revokedAt string
		if scanErr := rows.Scan(&tok.ID, &tok.Name, &tok.AgentID, &createdAt, &lastUsedAt, &revokedAt); scanErr != nil {
			return nil, fmt.Errorf("scan mcp token: %w", scanErr)
		}
		tok.CreatedAt = parseTime(createdAt)
		if lastUsedAt != "" {
			tok.LastUsedAt = parseTime(lastUsedAt)
		}
		if revokedAt != "" {
			tok.RevokedAt = parseTime(revokedAt)
		}
		out = append(out, &tok)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate mcp tokens: %w", rowsErr)
	}
	return out, nil
}

// RevokeMCPToken marks a token as revoked by its public ID (NOT the token
// digest — operators don't have access to digests). Idempotent: revoking
// an already-revoked token is a no-op.
func (s *SQLiteStore) RevokeMCPToken(ctx context.Context, id string) error {
	res, err := s.writeExecContext(ctx,
		`UPDATE mcp_tokens
		    SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		  WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke mcp token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either no such token, or already revoked. Distinguish for the caller.
		row := s.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_tokens WHERE id = ?`, id)
		var count int
		_ = row.Scan(&count)
		if count == 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}
