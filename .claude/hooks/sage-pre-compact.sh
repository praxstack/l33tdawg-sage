#!/bin/bash
# SAGE PreCompact hook — fires right before Claude Code compacts the
# conversation. Compaction discards turn-level detail; this is the last
# chance to crystallise what was learned this session.
SAGE_HOME="${SAGE_HOME:-$HOME/.sage}"
MODE=$(cat "$SAGE_HOME/memory_mode" 2>/dev/null || echo "full")
if [ "$MODE" = "on-demand" ]; then
    exit 0
fi
echo "MANDATORY before compaction: Call sage_reflect with a concise summary of (dos, don'ts) from this session, then sage_remember for any durable facts you want to keep. Once the context compacts, the per-turn detail is gone — only what you've committed to SAGE will survive."
