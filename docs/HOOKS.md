# Claude Code Lifecycle Hooks for SAGE

SAGE ships a small set of [Claude Code lifecycle hooks](https://docs.anthropic.com/en/docs/claude-code/hooks) that keep the agent's episodic memory in sync without depending on the agent to remember every step. They fire on session events and inject targeted reminders so calls to `sage_inception`, `sage_turn`, and `sage_reflect` happen at the right moments.

Available as of **v7.0**.

## Why hooks?

The agent's working memory lives in its context window. SAGE's persistent memory lives in the consensus-validated store. The bridge between the two is the agent calling `sage_turn` / `sage_reflect` at appropriate moments. In practice the agent forgets — especially mid-task, mid-compact, or at session end. Hooks close that gap by firing on the lifecycle event itself, regardless of whether the agent thought to act.

## What ships in this repo

The hooks under `.claude/` here are what the SAGE maintainers use day-to-day. You can copy them into your own project verbatim or pick and choose.

| Event | Script | Mode | What it does |
|---|---|---|---|
| `SessionStart` (startup, resume, compact) | `sage-session-start.sh` | **direct-write** | Signs a REST call to the local SAGE node, pre-fetches recent committed memories, and emits them as a context block the agent reads on boot. Falls back to the soft-nudge boot-check if the SAGE node isn't reachable or the agent key isn't readable. |
| `SessionEnd` | `sage-session-end.sh` | **direct-write** | Submits a `session-lifecycle` observation memory through full BFT consensus so the timeline shows session bookends. Soft-fails silently if SAGE isn't reachable — never blocks the agent's exit path. |
| `PreCompact` | `sage-pre-compact.sh` | nudge | Fires right before Claude Code compresses the context. Turn-level detail is about to be discarded — this nudge prompts the agent to call `sage_reflect` (and any `sage_remember` for durable facts) while context is still fresh. |
| `UserPromptSubmit` | `sage-user-prompt.sh` | nudge | Light reminder to call `sage_turn` early in the response, capturing the new conversational state. |
| `Stop` | `sage-stop.sh` | reserved | No-op placeholder. Fires per-turn, too high-frequency for direct-write without batching. |

### How the direct-write hooks work

Both direct-write scripts call into `.claude/hooks/lib/sage_direct.py`, which:

1. Reads the Ed25519 seed from `~/.sage/agent.key` (override with `SAGE_AGENT_KEY`).
2. Builds the canonical signed-request headers SAGE's REST middleware expects (`X-Agent-ID`, `X-Signature`, `X-Timestamp`).
3. POSTs / GETs against `http://localhost:8080` (override with `SAGE_URL`) with a tight 3 s timeout.
4. Soft-fails silently if any of those steps fail — the agent never sees an error from a missing SAGE node.

### Read scope on multi-agent nodes (v7.1)

Direct-write hooks sign with the **node operator's** Ed25519 key — that's what lives in `~/.sage/agent.key`. The on-chain identity that key resolves to is the operator, not the LLM agent (e.g. `claude-code/sage`) running this session.

As of v7.1 the SAGE REST layer recognises requests signed with the node operator's key and lets them bypass the cross-agent visibility filter on read paths. Concretely: `Server.SetNodeOperatorID` is wired at startup from `~/.sage/agent.key`, and `resolveVisibleAgents` short-circuits to `seeAll=true` when the caller matches. Per-domain access and per-record classification gates still apply, so the bypass doesn't lift hard access controls — it only lifts the agent-isolation filter that was making the SessionStart prefetch empty on multi-agent nodes.

If `~/.sage/agent.key` is missing or unreadable, the bypass stays off and the legacy RBAC behaviour applies. The fallback nudge in `sage-session-start.sh` continues to cover environments where direct read isn't available.

## Installing in your own project

Copy `.claude/hooks/*.sh` and `.claude/settings.json` from this repo into your project's `.claude/` directory. The hook commands are relative paths (`bash .claude/hooks/...`), so as long as you preserve the directory layout no edits are needed.

If your project already has a `.claude/settings.json`, merge the `hooks` block instead of replacing the file. The `hooks` object is keyed by event name; each event takes an array of matcher entries.

After copying, mark the scripts executable:

```bash
chmod +x .claude/hooks/*.sh
```

Restart your Claude Code session. The hooks fire automatically.

## Disabling individual hooks

Comment out or remove the matching event entry in `.claude/settings.json`. Hooks are opt-in per event, so dropping one doesn't affect the others.

## Mixed model

SAGE v7.0 ships **two SessionStart/SessionEnd direct-write hooks** plus **three nudge hooks** for the events where direct-write would be too noisy (`UserPromptSubmit`, `PreCompact`) or too high-frequency without batching (`Stop`). The mix lets capture happen automatically at the session boundary, while the conversation-level memory remains the agent's job (via `sage_turn`, `sage_reflect`) since only the LLM has enough context to distill what's actually worth remembering.

## Forward direction

- **v7.1** — broader read scope for the node-operator hook key so the SessionStart prefetch returns useful context on multi-agent nodes. Optional batched `PostToolUse` direct-write so tool calls auto-observe.
- **v7.x** — Codex CLI hook parity using the same event shape, then Cursor / Cline / Windsurf as those hosts expose lifecycle events.
