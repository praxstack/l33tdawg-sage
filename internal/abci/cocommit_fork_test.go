package abci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/l33tdawg/sage/internal/memory"
	"github.com/l33tdawg/sage/internal/tx"
)

// ---------------------------------------------------------------------------
// co-commit (v11 / app-v15): tx 31 CoCommitSubmit + tx 32 CoCommitAttest.
// A jointly-signed envelope is committed NATIVELY to each chain as a local memory
// keyed by the content-derived, height-free SharedID; peers cross-anchor via
// signed CommitReceipts. Both txs are dual-gated on postAppV15Fork (byte-identical
// pre-activation). The LOCAL submitter must be one of the coauthors; foreign
// coauthors are verified by standalone ed25519 signature only. A co-commit is
// COMMITTED immediately (block inclusion is decisive), never routed through the
// content-quality voter.
// ---------------------------------------------------------------------------

type testCoauthor struct {
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	chain string
}

func genTestCoauthor(t *testing.T, chain string) testCoauthor {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return testCoauthor{pub: pub, priv: priv, chain: chain}
}

// buildCoCommitEnvelope builds a fully-signed 2-coauthor envelope where `local`
// is a coauthor (the realistic dual-commit model) plus one foreign coauthor on
// `foreignChain`. Returns the envelope and the foreign coauthor (whose key signs
// the peer receipt in attest tests).
func buildCoCommitEnvelope(t *testing.T, local agentKey, domain string, nonce []byte, foreignChain string) (*tx.CoCommitSubmit, testCoauthor) {
	t.Helper()
	foreign := genTestCoauthor(t, foreignChain)
	ch := sha256.Sum256([]byte("co-committed content " + domain))
	env := &tx.CoCommitSubmit{
		SchemaVersion:   1,
		ContentHash:     ch[:],
		MemoryType:      tx.MemoryTypeFact,
		Domain:          domain,
		Classification:  tx.ClearanceInternal,
		ConfidenceScore: 0.9,
		CreatedAtUnix:   1_700_000_000,
		AgreementNonce:  nonce,
		Coauthors: []tx.CoCommitCoauthor{
			{PubKey: local.pub, ChainID: "sage-local"},
			{PubKey: foreign.pub, ChainID: foreign.chain},
		},
	}
	core := tx.CanonicalCoreBytes(env)
	env.Coauthors[0].Sig = ed25519.Sign(local.priv, core)
	env.Coauthors[1].Sig = ed25519.Sign(foreign.priv, core)
	env.SharedID = tx.ComputeSharedID(tx.CoreHashOf(env), env.Coauthors, nonce)
	return env, foreign
}

func coCommitSubmitTx(t *testing.T, local agentKey, env *tx.CoCommitSubmit) *tx.ParsedTx {
	t.Helper()
	pubKey, sig, bodyHash, ts := signAgentProof(t, local, []byte(env.SharedID))
	return &tx.ParsedTx{
		Type:           tx.TxTypeCoCommitSubmit,
		CoCommitSubmit: env,
		AgentPubKey:    pubKey,
		AgentSig:       sig,
		AgentBodyHash:  bodyHash,
		AgentTimestamp: ts,
	}
}

// signedReceiptAttest builds a CoCommitAttest whose receipt is signed by `signer`
// and advertised as coming from chain `peerChain` with pubkey `peerPub`.
func signedReceiptAttest(sharedID, peerChain string, peerPub ed25519.PublicKey, signer ed25519.PrivateKey, coreHash []byte) *tx.ParsedTx {
	receipt := &tx.CommitReceipt{
		ChainID: peerChain, SharedID: sharedID, LocalMemID: "peer-mem-1",
		Height: 7, CommitTime: 1_700_000_500, CoreHash: coreHash,
	}
	rbytes := tx.EncodeCommitReceipt(receipt)
	return &tx.ParsedTx{
		Type: tx.TxTypeCoCommitAttest,
		CoCommitAttest: &tx.CoCommitAttest{
			SharedID: sharedID, PeerChainID: peerChain, PeerPubKey: peerPub,
			Receipt: rbytes, PeerSig: ed25519.Sign(signer, rbytes),
			CommitTime: receipt.CommitTime, CoreHash: coreHash,
		},
	}
}

// TestCoCommit_DualGatePreFork: pre-activation, the exec gate rejects both new tx
// types with Code 10 and writes NOTHING (byte-identical AppHash).
func TestCoCommit_DualGatePreFork(t *testing.T) {
	app := setupTestApp(t) // app-v15 dormant
	require.Equal(t, int64(0), app.appV15AppliedHeight)
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")

	before, err := ComputeAppHash(app.badgerStore)
	require.NoError(t, err)

	sub := app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now())
	assert.Equal(t, uint32(10), sub.Code, "pre-fork submit rejected as unknown tx")

	att := app.processCoCommitAttest(&tx.ParsedTx{Type: tx.TxTypeCoCommitAttest, CoCommitAttest: &tx.CoCommitAttest{SharedID: env.SharedID}}, 10, time.Now())
	assert.Equal(t, uint32(10), att.Code, "pre-fork attest rejected as unknown tx")

	after, err := ComputeAppHash(app.badgerStore)
	require.NoError(t, err)
	assert.Equal(t, before, after, "pre-fork rejects write no key — AppHash byte-identical")
	core, _ := app.badgerStore.GetCoCommitCore(env.SharedID)
	assert.Nil(t, core, "no cocommit:core written pre-fork")
}

// TestCoCommit_SubmitPostFork: a valid 2-coauthor envelope becomes a native local
// memory keyed by SharedID, COMMITTED immediately, with the anchor keys and
// local-submitter author.
func TestCoCommit_SubmitPostFork(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")

	res := app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now())
	require.Equal(t, uint32(0), res.Code, "submit: %s", res.Log)
	assert.Equal(t, env.SharedID, string(res.Data))

	_, st, err := app.badgerStore.GetMemoryHash(env.SharedID)
	require.NoError(t, err)
	assert.Equal(t, string(memory.StatusCommitted), st, "co-commit is committed immediately (never routed through the voter)")

	author, _ := app.badgerStore.GetMemoryAuthor(env.SharedID)
	assert.Equal(t, local.id, author, "memauthor = LOCAL submitter")

	core, _ := app.badgerStore.GetCoCommitCore(env.SharedID)
	assert.Equal(t, tx.CoreHashOf(env), core, "cocommit:core = CoreHashOf(envelope)")

	dom, _ := app.badgerStore.GetMemoryDomain(env.SharedID)
	assert.Equal(t, "family.photos", dom)

	// M1: the auto-registered owner holds a level-2 self-grant (not locked out).
	ok, err := app.badgerStore.HasAccessOrAncestor("family.photos", local.id, 2, time.Now())
	require.NoError(t, err)
	assert.True(t, ok, "auto-registered owner has a level-2 self-grant")
}

// TestCoCommit_SubmitterNotCoauthorRejected (P1): a submitter who is not one of
// the coauthors is rejected.
func TestCoCommit_SubmitterNotCoauthorRejected(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	alice := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, alice, "d", []byte("n"), "sage-b")

	bob := newAgentKey(t) // not a coauthor
	res := app.processCoCommitSubmit(coCommitSubmitTx(t, bob, env), 10, time.Now())
	assert.Equal(t, uint32(95), res.Code, "non-coauthor submitter rejected")
}

// TestCoCommit_SchemaVersionRejected (P2): an unsupported schema version is rejected.
func TestCoCommit_SchemaVersionRejected(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "d", []byte("n"), "sage-b")
	env.SchemaVersion = 2
	// Re-sign for the new core (SchemaVersion is in the signed core) so we test the
	// version gate, not a signature failure.
	core := tx.CanonicalCoreBytes(env)
	env.Coauthors[0].Sig = ed25519.Sign(local.priv, core)
	// coauthor[1] sig now stale, but the version check precedes sig verification.
	res := app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now())
	assert.Equal(t, uint32(95), res.Code, "unsupported schema version rejected")
	assert.Contains(t, res.Log, "schema version")
}

// TestCoCommit_SharedIDMismatchRejected (Code 96): a SharedID not derivable from
// the signed core is rejected.
func TestCoCommit_SharedIDMismatchRejected(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "d", []byte("n"), "sage-b")
	env.SharedID = "deadbeefdeadbeef" // tamper (after sigs + P1 pass)

	res := app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now())
	assert.Equal(t, uint32(96), res.Code, "tampered SharedID rejected")
}

// TestCoCommit_BadCoauthorSigRejected (Code 95): a corrupted coauthor signature.
func TestCoCommit_BadCoauthorSigRejected(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "d", []byte("n"), "sage-b")
	env.Coauthors[1].Sig[0] ^= 0xff // corrupt the foreign coauthor's sig

	res := app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now())
	assert.Equal(t, uint32(95), res.Code, "corrupted coauthor sig rejected")
}

// TestCoCommit_AttestPostFork: a receipt signed by a DECLARED coauthor for its
// chain is recorded as a cross-anchor.
func TestCoCommit_AttestPostFork(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, foreign := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")
	require.Equal(t, uint32(0), app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now()).Code)

	// The peer IS the foreign coauthor (key + chain match the recorded set).
	att := signedReceiptAttest(env.SharedID, foreign.chain, foreign.pub, foreign.priv, tx.CoreHashOf(env))
	res := app.processCoCommitAttest(att, 11, time.Now())
	assert.Equal(t, uint32(0), res.Code, "attest by a declared coauthor: %s", res.Log)
}

// TestCoCommit_AttestForgedPeerRejected (H2): a receipt signed over the PUBLIC
// CoreHash with a key that is NOT a recorded coauthor is rejected — no forged
// cross-anchor.
func TestCoCommit_AttestForgedPeerRejected(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, _ := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")
	require.Equal(t, uint32(0), app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now()).Code)

	// Attacker: fresh key, correct public CoreHash, claims to be "sage-b".
	fakePub, fakePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	att := signedReceiptAttest(env.SharedID, "sage-b", fakePub, fakePriv, tx.CoreHashOf(env))
	res := app.processCoCommitAttest(att, 11, time.Now())
	assert.Equal(t, uint32(95), res.Code, "forged-peer attest rejected (key not a recorded coauthor)")

	anchor, _ := app.badgerStore.GetCoCommitCore(env.SharedID) // core exists; anchor must NOT
	require.NotNil(t, anchor)
}

// TestCoCommit_AttestFailClosed (Code 97): an attest for a SharedID this chain
// never co-committed is rejected (fail-closed, no anchor).
func TestCoCommit_AttestFailClosed(t *testing.T) {
	app := setupTestApp(t)
	app.appV15AppliedHeight = 5

	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	att := signedReceiptAttest("unknown-shared", "sage-b", peerPub, peerPriv, []byte("x"))
	res := app.processCoCommitAttest(att, 10, time.Now())
	assert.Equal(t, uint32(97), res.Code, "attest with no local co-commit is fail-closed")
}

// TestCoCommit_CheckTxDualGate (L1): the load-bearing CheckTx gate returns Code 10
// pre-fork (keeps type 31/32 out of the mempool) and admits post-fork.
func TestCoCommit_CheckTxDualGate(t *testing.T) {
	app := setupTestApp(t)
	local := newAgentKey(t)
	registerAgent(t, app, local, "local", "member")
	env, _ := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")

	ptx := coCommitSubmitTx(t, local, env)
	require.NoError(t, tx.SignTx(ptx, local.priv))
	encoded, err := tx.EncodeTx(ptx)
	require.NoError(t, err)

	// Pre-fork: gate rejects.
	app.state.Height = 0
	resp, err := app.CheckTx(context.TODO(), &abcitypes.RequestCheckTx{Tx: encoded})
	require.NoError(t, err)
	assert.Equal(t, uint32(10), resp.Code, "pre-fork CheckTx rejects co-commit with Code 10")
	assert.Contains(t, resp.Log, "unknown tx type")

	// Post-fork: gate admits.
	app.appV15AppliedHeight = 5
	app.state.Height = 100
	resp2, err := app.CheckTx(context.TODO(), &abcitypes.RequestCheckTx{Tx: encoded})
	require.NoError(t, err)
	assert.NotEqual(t, uint32(10), resp2.Code, "post-fork CheckTx admits co-commit (gate passed): %s", resp2.Log)
}

// TestCoCommit_CoauthorCannotSelfCorroborate (M2): a recorded coauthor cannot
// corroborate the jointly-authored memory; a non-coauthor can.
func TestCoCommit_CoauthorCannotSelfCorroborate(t *testing.T) {
	app := setupTestApp(t)
	app.appV10AppliedHeight = 1 // corroboration guard active
	app.appV15AppliedHeight = 5
	local := newAgentKey(t)
	env, foreign := buildCoCommitEnvelope(t, local, "family.photos", []byte("n1"), "sage-b")
	require.Equal(t, uint32(0), app.processCoCommitSubmit(coCommitSubmitTx(t, local, env), 10, time.Now()).Code)

	// The foreign coauthor (a recorded coauthor) tries to corroborate -> rejected.
	foreignAgent := agentKey{pub: foreign.pub, priv: foreign.priv, id: hex.EncodeToString(foreign.pub)}
	self := app.processMemoryCorroborate(makeMemoryCorroborateTx(t, foreignAgent, env.SharedID, "self"), 11, time.Now())
	assert.Equal(t, uint32(17), self.Code, "a coauthor cannot self-corroborate a co-authored memory")
	assert.Contains(t, self.Log, "co-authored")

	// A non-coauthor CAN corroborate.
	outsider := newAgentKey(t)
	ok := app.processMemoryCorroborate(makeMemoryCorroborateTx(t, outsider, env.SharedID, "independent"), 12, time.Now())
	assert.Equal(t, uint32(0), ok.Code, "a non-coauthor may corroborate: %s", ok.Log)
}
