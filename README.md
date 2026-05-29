# (S)AGE — Sovereign Agent Governed Experience

**Persistent, consensus-validated memory infrastructure for AI agents.**

SAGE gives AI agents institutional memory that persists across conversations, goes through BFT consensus validation, carries confidence scores, and decays naturally over time. Not a flat file. Not a vector DB bolted onto a chat app. Infrastructure — built on the same consensus primitives as distributed ledgers.

The architecture is described in [Paper 1: Agent Memory Infrastructure](papers/Paper1%20-%20Agent%20Memory%20Infrastructure%20-%20Byzantine-Resilient%20Institutional%20Memory%20for%20Multi-Agent%20Systems.pdf).

> **Just want to install it?** [Download here](https://l33tdawg.github.io/sage/) — double-click, done. Works with any AI.

<a href="https://glama.ai/mcp/servers/l33tdawg/s-age">
  <img width="380" height="200" src="https://glama.ai/mcp/servers/l33tdawg/s-age/badge" alt="(S)AGE MCP server" />
</a>

---

## Architecture

```
Agent (Claude, ChatGPT, DeepSeek, Gemini, etc.)
  │ MCP / REST
  ▼
sage-gui
  ├── ABCI App (validation, confidence, decay, Ed25519 sigs)
  ├── App Validators (sentinel, dedup, quality, consistency — BFT 3/4 quorum)
  ├── Governance Engine (on-chain validator proposals + voting)
  ├── CometBFT consensus (single-validator or multi-agent network)
  ├── SQLite + optional AES-256-GCM encryption
  ├── CEREBRUM Dashboard (SPA, real-time SSE)
  └── Network Agent Manager (add/remove agents, key rotation, LAN pairing)
```

Personal mode runs a real CometBFT node with 4 in-process application validators — every memory write goes through pre-validation, signed vote transactions, and BFT quorum before committing. Same consensus pipeline as multi-node deployments. Add more agents from the dashboard when you're ready.

Full deployment guide (multi-agent networks, RBAC, federation, monitoring): **[Architecture docs](docs/ARCHITECTURE.md)**

---

## CEREBRUM Dashboard

![CEREBRUM — Neural network memory visualization](docs/screen-brain.png)

`http://localhost:8080/ui/` — force-directed neural graph, domain filtering, semantic search, real-time updates via SSE.

### Network Management

![Network — Multi-agent management](docs/screen-network.png)

Add agents, configure domain-level read/write permissions, manage clearance levels, rotate keys, download bundles — all from the dashboard.

### Settings

| Overview | Security | Configuration | Update |
|:---:|:---:|:---:|:---:|
| ![Overview](docs/screen-overview.png) | ![Security](docs/screen-security.png) | ![Config](docs/screen-config.png) | ![Update](docs/screen-update.png) |
| Chain health, peers, system status | Synaptic Ledger encryption, export | Boot instructions, cleanup, tooltips | One-click updates from dashboard |

---

## What's New in v8.3

Real PoE signals. v8.2 made quorum *consult* each validator's PoE weight, but two of the four factors feeding `poe.ComputeWeight` were still Phase-1 placeholders: accuracy was a cold-start accept-ratio blend (rewarding validators for voting "accept", not for being *right*), and corroboration was a hardcoded default. v8.3 makes both real, behind a single fork (`app-v4`). Pre-fork blocks (and any v8.2.x chain) replay byte-identical to v8.2.1; the watchdog auto-proposes activation at `app-v4`, the fork takes effect at H+1.

- **Accuracy is now verdict-correctness, not accept-rate.** When a memory reaches a terminal verdict (committed or deprecated), `checkAndApplyQuorum` credits each voting validator with whether its vote *matched the final committed verdict* — fed into a per-validator EWMA (`poe.EWMATracker`, η=0.9, cold-start prior 0.5, blended to full weight at 10 observations). A validator that reliably votes with the eventual consensus earns a high accuracy; one that dissents from correct verdicts decays. Post-fork `processEpoch` reads this EWMA instead of the accept-ratio blend.
- **Corroboration is now a real count.** `CorrCount` accumulates each validator's lifetime verdict matches and feeds `ComputeWeight`'s S term (`log(1+count)/log(1+CorrMax)`) instead of the Phase-1 zero.
- **Both signals share one event and one fork.** They're credited from the *same* proposed→terminal transition, so they share the `app-v4` gate and one on-chain encoding change: `vstats:<id>` records grow 24 → 56 bytes (three EWMA accumulators + the corroboration count appended as IEEE-754 / big-endian uint64), with a lazy per-validator migration on the first post-fork write and length-dispatch decode so legacy 24-byte records read as the cold-start priors. `recordV8_3Branch` emits `sage_fork_branch_total{fork="v8.3",branch="pre|post"}`.
- **Credited exactly once, fail-closed.** The verdict-match credit fires only on the first transition *into* a terminal state — `checkAndApplyQuorum` captures the memory's prior status (via `GetMemoryHash`) at the top of the call, *before* any `SetMemoryHash` write, so a replayed vote on an already-committed memory credits nothing. Any read error leaves the prior status empty, never mis-crediting. The challenge path doesn't reach the hook, so challenges never credit.
- **Off-chain mirror kept honest.** Post-fork, the REST `/v1/agent` accuracy is sourced from the same on-chain EWMA fields that drive quorum weight, so the operator-facing number matches consensus rather than diverging to the old accept-ratio.
- **One-time intended reweighting at activation.** Accuracy starts at the 0.5 cold-start prior at `app-v4` activation and re-accrues from real verdicts — a deliberate reset of the (previously accept-ratio-derived) accuracy term, noted in the release.
- **Test coverage.** Store-side V1-V6 pin the 56-byte codec (golden bytes incl. `0.25 → 3F D0 00 …`), legacy decode, the verdict-EWMA math, batch atomicity, and the lazy 24→56 migration. Quorum AV1-AV5 prove commit credits matchers, a deprecate-tie credits the rejecters, idempotency under replayed votes, pre-fork no-crediting, and that `processEpoch` consumes the signals. An end-to-end test drives a real `app-v4` activation: pre-fork all validators score 0.65 accuracy / 0 corroboration, post-fork the consensus-aligned validators reach 0.70 / 0.53 while a dissenter drops to 0.30.

## Older releases

<details>
<summary>v8.2 — PoE-weighted quorum activation</summary>

- **v8.2 — PoE weights drive quorum.** The PoE engine had computed per-validator engagement scores every epoch since v6.x, but `checkAndApplyQuorum` ignored them and used a hardcoded `weights[v.ID] = 1.0` — quorum was a 2/3 *majority*, not a 2/3 *weighted vote*. v8.2 closed that with a single fork-gated swap (`app-v3`): post-fork quorum consults `v.PoEWeight` via `app.postV8_2Fork(height)`; the normalized weight set is persisted on-chain every epoch (`poew:current` + `poew:<id>`, pruned on governance set changes, rehydrated on restart); `poeWeightOrFallback` returns `1/N` for pre-first-epoch / mid-epoch-add / missing-entry cases, keeping the fallback in `NormalizeWeights`' numeric range without moving the ratio-only 2/3 threshold. Bundled CometBFT v0.38.15 → v0.38.23 (GHSA-hrhf-2vcr-ghch + blocksync/nil-vote/ABCI-socket hardening). Pre-fork byte-identical to v8.1.2; 16 new tests + a 4-validator devnet held byte-identical AppHash for 160+ blocks across two epoch boundaries.

</details>

<details>
<summary>Capability milestones across v3–v7 (full per-patch detail on the <a href="https://github.com/l33tdawg/sage/releases">Releases page</a>)</summary>

- **v8.1 — Governance + ancestor cleanup + O(1) per-block AppHash.** Three follow-up fixes after v8.0 surfaced edges: postgres quorum register-agent consensus halt (8.0.1), postgres quorum governance mirror (8.1.0), per-record clearance arg on SDK `propose()` (8.1.1), governance/ancestor walk cleanups + `ComputeAppHash` switched from `O(state)` per-block alloc to streaming SHA-256 over a lex-sorted iterator (8.1.2). Single-machine personal mode no longer churns GC pressure linearly with chain height.
- **v8.0 — Access-control consensus cleanup.** Three real bugs from v7.1 fixed behind a single fork (`app-v2`): subdomain grants now cascade via `HasAccessOrAncestor`, granting on an unowned domain auto-claims it, and `TxTypeDomainReassign` recovers lost-owner domains via a 3/4-supermajority gov proposal. Pre-fork byte-identical to v7.1.1. Python SDK 8.0.0 adds `submit_domain_reassign` + a high-level `reassign_domain` helper.
- **v7.7 — Agent profile fill-in.** `GET /v1/agent/me` now returns the full profile the OpenAPI schema promised — `display_name`, `domains`, `accuracy`, `on_chain_height` — so SDK consumers don't round-trip to `/v1/agent/{id}` plus the validator-score endpoint just to render a profile card.
- **v7.6 — Direct-write hooks for Claude Code and Codex.** `sage-gui hook session-start | session-end` signs REST calls to the local SAGE node directly; `mcp install` and `codex install` ship the unified 5-script lifecycle set; selfHeal migrates legacy installs and auto-installs hooks on MCP boot for pre-v7.6 projects (v7.6.2).
- **v7.5 — Migration substrate.** Hands-off in-place chain upgrades — scheduled snapshots with verify-by-restore, upgrade tx types with chain-computed activation height, auto-proposal watchdog, HALT sentinel + supervised rollback. v7.5.0 itself ships zero consensus-rule changes; it's the plumbing every later release rides on.
- **v7.1 — Recall quality + second benchmark.** Optional cross-encoder reranking and query expansion on `/v1/memory/hybrid`, LoCoMo benchmark (R@5=0.6394 stock), SAGE adapter shipped upstream to mem0's open-source evaluator. v7.1.1 closed the silent ghost-tx surface on RBAC/governance writes.
- **v7.0 — Hybrid recall + ambient capture.** BM25 + vector fused via Reciprocal Rank Fusion on a new `/v1/memory/hybrid` endpoint, direct-write lifecycle hooks for Claude Code, branch-aware memory tagging, LongMemEval-S benchmark at R@5=0.9053.
- **v6.8 — Hardening pass.** OAuth Dynamic Client Registration + persistent client metadata, mandatory `state` + HMAC-signed CSRF on `/oauth/authorize`, strict same-origin on CEREBRUM wizard endpoints, locked-down subprocess test seams. Admin-bootstrap escape hatch (6.8.5), cross-agent visibility hotfix (6.8.4), Windows wizard parity (6.8.1).
- **v6.7 — ChatGPT MCP connector.** OAuth 2.0 + PKCE wrapper, RFC 8414/7591/9728 discovery and Dynamic Client Registration, in-dashboard ChatGPT setup wizard (6.7.3, Cloudflare zone dropdown 6.7.4), HTTPS-capable HTTP MCP transport (`/v1/mcp/sse` + `/v1/mcp/streamable` on `:8443`) with bearer tokens.
- **v6.6 — Tags + multi-org + RBAC fixes.** Tags first-class on `/v1/memory/submit` and `/query`/`/search` filtering. Multi-org membership reverse index so agents in N orgs no longer silently lose access to N-1 of them. `PUT /v1/agent/{id}/permission` no longer silent-no-ops for non-admin self/org-admin callers. SQLITE_BUSY silent-drop fix at source (WAL pragma + writeMu-guarded BeginTx). Encrypted CA key in quorum manifest (Argon2id + AES-256-GCM envelope).
- **v6.5 — TLS everywhere.** Per-quorum ECDSA P-256 CA, dual-listener REST API (TLS `:8443` + local HTTP `:8080`), Python SDK `ca_cert` parameter. Stuck-proposed deprecation when quorum unreachable. RBAC ownership-theft fix + real broadcast errors surfaced to clients.
- **v6.0 — Dynamic validator governance.** Add/remove/repower validators without stopping the chain via on-chain governance proposals (2/3 BFT quorum). New `internal/governance/` package, in-dashboard Governance section.
- **v5.x — Consensus-first writes + FTS5.** All submissions go through BFT consensus before they surface in queries. 4-validator Docker cluster with fault injection in CI. FTS5 keyword search fallback. Nonce-based replay protection. Python SDK.
- **v4.x — App validators + RBAC + Synaptic Ledger.** Sentinel / Dedup / Quality / Consistency validators with 3/4 quorum. Agent isolation, domain-level permissions, clearance levels, multi-org federation. AES-256-GCM encryption with Argon2id key derivation.
- **v3.x — Multi-agent networks.** Add agents from dashboard, LAN pairing, key rotation, redeployment orchestrator. On-chain agent identity via CometBFT consensus. CEREBRUM dashboard.

</details>

---

## Research

| Paper | Key Result |
|-------|------------|
| [Agent Memory Infrastructure](papers/Paper1%20-%20Agent%20Memory%20Infrastructure%20-%20Byzantine-Resilient%20Institutional%20Memory%20for%20Multi-Agent%20Systems.pdf) | BFT consensus architecture for agent memory |
| [Consensus-Validated Memory](papers/Paper2%20-%20Consensus-Validated%20Memory%20Improves%20Agent%20Performance%20on%20Complex%20Tasks.pdf) | 50-vs-50 study: memory agents outperform memoryless |
| [Institutional Memory](papers/Paper3%20-%20Institutional%20Memory%20as%20Organizational%20Knowledge%20-%20AI%20Agents%20That%20Learn%20Their%20Jobs%20from%20Experience%20Not%20Instructions.pdf) | Agents learn from experience, not instructions |
| [Longitudinal Learning](papers/Paper4%20-%20Longitudinal%20Learning%20in%20Governed%20Multi-Agent%20Systems%20-%20How%20Institutional%20Memory%20Improves%20Agent%20Performance%20Over%20Time.pdf) | Cumulative learning: rho=0.716 with memory vs 0.040 without |

---

## Quick Start

```bash
git clone https://github.com/l33tdawg/sage.git && cd sage
go build -o sage-gui ./cmd/sage-gui/
./sage-gui setup    # Pick your AI, get MCP config
./sage-gui serve    # SAGE + Dashboard on :8080
```

Or grab a binary: [macOS DMG](https://github.com/l33tdawg/sage/releases/latest) (signed & notarized) | [Windows EXE](https://github.com/l33tdawg/sage/releases/latest) | [Linux tar.gz](https://github.com/l33tdawg/sage/releases/latest)

### Docker

```bash
docker pull ghcr.io/l33tdawg/sage:latest
docker run -p 8080:8080 -v ~/.sage:/root/.sage ghcr.io/l33tdawg/sage:latest
```

Pin a specific version with `ghcr.io/l33tdawg/sage:6.0.0`.

### Upgrading from an older version?

If you installed SAGE before v5.0 and your AI isn't doing turn-by-turn memory updates, re-run the installer in your project directory:

```bash
cd /path/to/your/project
sage-gui mcp install
```

This installs Claude Code hooks that enforce the memory lifecycle (boot, turn, reflect) — even if your `.mcp.json` is already configured. Restart your Claude Code session after running this.

---

## Documentation

| Doc | What's in it |
|-----|-------------|
| [Architecture & Deployment](docs/ARCHITECTURE.md) | Multi-agent networks, BFT, RBAC, federation, API reference |
| [Getting Started](docs/GETTING_STARTED.md) | Setup walkthrough, embedding providers, multi-agent network guide |
| [Security FAQ](SECURITY_FAQ.md) | Threat model, encryption, auth, signature scheme |
| [Connect Your AI](https://l33tdawg.github.io/sage/connect.html) | Interactive setup wizard for any provider |

---

## Stack

Go / CometBFT v0.38 / chi / SQLite / Ed25519 + AES-256-GCM + Argon2id / MCP

---

## License

Code: [Apache 2.0](LICENSE) | Papers: [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)

## Author

Dhillon Andrew Kannabhiran ([@l33tdawg](https://github.com/l33tdawg))

---

<p align="center"><em>A tribute to <a href="http://phenoelit.darklab.org/fx.html">Felix 'FX' Lindner</a> — who showed us <b>how much further curiosity can go.</b></em></p>
