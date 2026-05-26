#!/bin/bash
# SAGE UserPromptSubmit hook — fires when the user submits a new prompt.
# Soft nudge so the agent calls sage_turn early in its response.
SAGE_HOME="${SAGE_HOME:-$HOME/.sage}"
MODE=$(cat "$SAGE_HOME/memory_mode" 2>/dev/null || echo "full")
if [ "$MODE" = "on-demand" ] || [ "$MODE" = "bookend" ]; then
    exit 0
fi
echo "Reminder: call sage_turn early in your response with the topic + an observation of what just happened. Memories you don't store don't survive."
