package federation

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"sync"
	"time"
)

// JoinSession is the HOST-side server state for one bilateral join ceremony
// (the host runs the listener; the guest POSTs /fed/v1/join/*). It carries the
// RT-6 freeze fields locked at approval #1 so the host can never broadcast
// against an attestation no human compared, and binds to exactly one guest
// identity + TLS client cert after the first request (RT-2/RT-5).
type JoinSession struct {
	ID          string
	State       string
	LocalChain  string // host chain
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConfirmStep int64

	// Host-side material (known at Create).
	HostPin      []byte
	HostEndpoint string
	Seed         []byte
	HostNonce    []byte

	// Guest-side material (bound at the first Request — one-guest-identity).
	GuestChain    string
	GuestPin      []byte
	GuestEndpoint string
	GuestNonce    []byte
	GuestScope    [32]byte
	HostScope     [32]byte
	BoundCertSPKI []byte // RT-2/RT-5: the TLS client-cert SPKI bound to this session

	// RT-6 freeze (locked at HOST_APPROVED).
	Approved           bool
	ApprovedE          [32]byte
	ApprovedGuestPin   []byte
	ApprovedGuestNonce []byte

	failCount int
}

// Join session states (§6.2).
const (
	JoinCreated        = "CREATED"
	JoinRequested      = "REQUESTED"
	JoinHostApproved   = "HOST_APPROVED"
	JoinGuestConfirmed = "GUEST_CONFIRMED"
	JoinActive         = "ACTIVE"
	JoinAborted        = "ABORTED"
	JoinExpired        = "EXPIRED"
)

const (
	joinSessionTTL    = 15 * time.Minute
	joinSessionIDLen  = 8 // 64 bits ≥ the 40-bit RT-3 floor
	joinMaxFailPerSid = 8 // per-session-id fail cap (RT-3)
)

var joinB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// JoinStore is the host-side session registry: TTL'd, single-ceremony, with a
// per-session fail cap and a TLS-connection rate limiter (never XFF, RT-3).
type JoinStore struct {
	mu       sync.Mutex
	sessions map[string]*JoinSession
	rl       *connRateLimiter
}

func NewJoinStore() *JoinStore {
	return &JoinStore{sessions: make(map[string]*JoinSession), rl: newConnRateLimiter()}
}

// Create opens a host-side session and returns it (state CREATED). The host
// then emits the enrollment QR carrying session_id + host_pin + endpoint + seed.
func (s *JoinStore) Create(localChain string, hostPin []byte, hostEndpoint string, seed []byte, now time.Time) (*JoinSession, error) {
	idb := make([]byte, joinSessionIDLen)
	if _, err := rand.Read(idb); err != nil {
		return nil, fmt.Errorf("join session id: %w", err)
	}
	hn := make([]byte, 16)
	if _, err := rand.Read(hn); err != nil {
		return nil, err
	}
	js := &JoinSession{
		ID: joinB32.EncodeToString(idb), State: JoinCreated,
		LocalChain: localChain, CreatedAt: now, ExpiresAt: now.Add(joinSessionTTL),
		HostPin: append([]byte(nil), hostPin...), HostEndpoint: hostEndpoint,
		Seed: append([]byte(nil), seed...), HostNonce: hn,
	}
	s.mu.Lock()
	s.sessions[js.ID] = js
	s.mu.Unlock()
	return js, nil
}

// SessionID returns the base32 id for the QR (x_sage_sid).
func (js *JoinSession) SessionID() string { return js.ID }

// Get returns a live (non-expired) session, expiring it lazily.
func (s *JoinStore) Get(id string, now time.Time) (*JoinSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if now.After(js.ExpiresAt) && js.State != JoinActive {
		js.State = JoinExpired
		return nil, false
	}
	return js, true
}

// AllowConn reports whether a request from connKey (a TLS-connection-derived
// key, NOT XFF) is within the rate limit.
func (s *JoinStore) AllowConn(connKey string, now time.Time) bool { return s.rl.allow(connKey, now) }

// Request binds the guest identity + TLS cert to the session on the first call
// (one-guest-identity, RT-2/RT-5), picks the confirm step, and moves to
// REQUESTED. A second request with a DIFFERENT guest identity/cert is refused;
// a re-request that mutates guest fields after approval resets the approval
// (enforced in Approve/Confirm via the frozen check).
func (s *JoinStore) Request(id string, now time.Time, guestChain string, guestNonce, guestPin []byte, guestEndpoint string, guestScope, hostScope [32]byte, certSPKI []byte) (*JoinSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.sessions[id]
	if !ok || (now.After(js.ExpiresAt) && js.State != JoinActive) {
		return nil, fmt.Errorf("join session not found or expired")
	}
	if js.failCount >= joinMaxFailPerSid {
		return nil, fmt.Errorf("join session locked (too many attempts)")
	}
	// One-guest-identity binding: after the first request, a different guest
	// chain or cert is refused (a distinct guest needs a distinct session).
	if js.GuestChain != "" {
		if js.GuestChain != guestChain || subtle.ConstantTimeCompare(js.BoundCertSPKI, certSPKI) != 1 {
			js.failCount++
			return nil, fmt.Errorf("join session already bound to a different guest")
		}
	}
	// A re-request that changes guest material after an approval resets it
	// (RT-6: a resumed/regenerated session needs a fresh human compare).
	if js.Approved && (subtle.ConstantTimeCompare(js.GuestPin, guestPin) != 1 ||
		subtle.ConstantTimeCompare(js.GuestNonce, guestNonce) != 1) {
		js.Approved = false
		js.ApprovedE = [32]byte{}
	}
	js.GuestChain = guestChain
	js.GuestNonce = append([]byte(nil), guestNonce...)
	js.GuestPin = append([]byte(nil), guestPin...)
	js.GuestEndpoint = guestEndpoint
	js.GuestScope = guestScope
	js.HostScope = hostScope
	js.BoundCertSPKI = append([]byte(nil), certSPKI...)
	if js.ConfirmStep == 0 {
		js.ConfirmStep = time.Now().Unix() / 30 // frozen for the whole join window
	}
	if js.State == JoinCreated {
		js.State = JoinRequested
	}
	return js, nil
}

// Approve records approval #1 and FREEZES the attestation identity (RT-6). The
// host has already compared CODE_G. e is the attestation computed from the
// current (bound) session fields.
func (s *JoinStore) Approve(id string, e [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.sessions[id]
	if !ok || js.State == JoinExpired || js.State == JoinAborted {
		return fmt.Errorf("join session not approvable")
	}
	js.Approved = true
	js.ApprovedE = e
	js.ApprovedGuestPin = append([]byte(nil), js.GuestPin...)
	js.ApprovedGuestNonce = append([]byte(nil), js.GuestNonce...)
	js.State = JoinHostApproved
	return nil
}

// CheckConfirm verifies a confirm against the FROZEN attestation (RT-6): the
// approval must stand and the session's current guest fields must equal the
// frozen ones. Returns the frozen E to verify the guest ack signature against.
func (s *JoinStore) CheckConfirm(id string) (*JoinSession, [32]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.sessions[id]
	if !ok {
		return nil, [32]byte{}, fmt.Errorf("join session not found")
	}
	if !js.Approved || js.State != JoinHostApproved {
		return nil, [32]byte{}, fmt.Errorf("join session not host-approved")
	}
	// Reject on ANY mutation of the guest fields since approval.
	if subtle.ConstantTimeCompare(js.ApprovedGuestPin, js.GuestPin) != 1 ||
		subtle.ConstantTimeCompare(js.ApprovedGuestNonce, js.GuestNonce) != 1 {
		js.Approved = false
		js.ApprovedE = [32]byte{}
		js.State = JoinRequested
		return nil, [32]byte{}, fmt.Errorf("guest identity changed since approval — re-confirm required")
	}
	return js, js.ApprovedE, nil
}

// MarkActive transitions to ACTIVE after the host broadcasts its tx-33.
func (s *JoinStore) MarkActive(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if js, ok := s.sessions[id]; ok {
		js.State = JoinActive
	}
}

// Abort marks a session aborted (burned on a human "No" or an error).
func (s *JoinStore) Abort(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if js, ok := s.sessions[id]; ok {
		js.State = JoinAborted
	}
}

// Fail increments the per-session fail cap and returns whether it is now locked.
func (s *JoinStore) Fail(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.sessions[id]
	if !ok {
		return true
	}
	js.failCount++
	return js.failCount >= joinMaxFailPerSid
}

// OpenSessions returns the sessions still in flight (for the
// verifyFederationClientCert extension: their staged guest CAs authenticate).
func (s *JoinStore) OpenSessions() []*JoinSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*JoinSession, 0, len(s.sessions))
	now := time.Now()
	for _, js := range s.sessions {
		if now.After(js.ExpiresAt) {
			continue
		}
		if js.State == JoinAborted || js.State == JoinExpired || js.State == JoinActive {
			continue
		}
		out = append(out, js)
	}
	return out
}

// Cleanup drops expired/terminal sessions.
func (s *JoinStore) Cleanup(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, js := range s.sessions {
		if now.After(js.ExpiresAt.Add(joinSessionTTL)) || js.State == JoinAborted {
			delete(s.sessions, id)
		}
	}
}

// connRateLimiter is the RT-3 rate limiter — keyed on a TLS-connection-derived
// value the caller supplies (never X-Forwarded-For). Same token-bucket shape as
// web/pairing.go's redeemRateLimiter, re-keyed safely for the direct-connect
// federation listener.
type connRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

const (
	connMaxAttempts = 20
	connWindow      = 1 * time.Minute
)

func newConnRateLimiter() *connRateLimiter {
	return &connRateLimiter{attempts: make(map[string][]time.Time)}
}

func (rl *connRateLimiter) allow(key string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := now.Add(-connWindow)
	fresh := rl.attempts[key][:0]
	for _, t := range rl.attempts[key] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= connMaxAttempts {
		rl.attempts[key] = fresh
		return false
	}
	rl.attempts[key] = append(fresh, now)
	return true
}
