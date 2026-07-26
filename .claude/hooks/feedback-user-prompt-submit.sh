#!/usr/bin/env bash
# feedback-user-prompt-submit.sh — the sync half of the in-session feedback
# tap (ferret-wf9.1 + ferret-j33): on every human turn, first settle the
# PRIOR turn's business (answer: recognize dk's leading y/n/s token against
# any armed ask, classify valence, write the gold label), then read the
# pending-ask bank (if any), check the shared ask budget (feedback.Reserve),
# and inject the banked question via additionalContext when granted.
#
# Registered as a plain (synchronous) UserPromptSubmit hook (.claude/
# settings.json) with a short timeout — this does NO transcript parsing and
# calls NO LLM; it is one small file read plus one flock, well inside budget
# (see the plan's Design section, "The Stop-hook / UserPromptSubmit split").
#
# Always exits 0: a denied/absent ask is not a failure, it's the common case.

set -uo pipefail

INPUT=$(cat)
SESSION_ID=$(jq -r '.session_id // empty' <<<"$INPUT" 2>/dev/null)
if [ -z "$SESSION_ID" ] || ! command -v ferret >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
  echo '{}'
  exit 0
fi

# ferret-j33: settle the PRIOR turn's armed ask (if any) before deciding this
# turn's. Field is `.prompt` (confirmed against six already-deployed working
# UserPromptSubmit hooks in the sibling trixi repo — NOT `.user_prompt`).
# Best-effort, non-blocking (`|| true`): a failure here must never stop dk's
# turn. Both stdout and stderr are discarded — `answer` is a side-effecting
# call with nothing to contribute to this script's single-JSON-blob contract,
# and any stray stdout would corrupt it.
PROMPT=$(jq -r '.prompt // empty' <<<"$INPUT" 2>/dev/null)
ferret feedback answer --session "$SESSION_ID" <<<"$PROMPT" >/dev/null 2>&1 || true

CHECK_OUT=$(ferret feedback check --session "$SESSION_ID" 2>/dev/null) || { echo '{}'; exit 0; }

ASK=$(jq -r '.ask // false' <<<"$CHECK_OUT" 2>/dev/null)
if [ "$ASK" != "true" ]; then
  echo '{}'
  exit 0
fi

QUESTION=$(jq -r '.question // empty' <<<"$CHECK_OUT")
if [ -z "$QUESTION" ]; then
  echo '{}'
  exit 0
fi

# Mirrors context-checkpoint.sh / inject-manifest.sh's jq -nc envelope idiom.
jq -nc --arg q "$QUESTION" \
  '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: $q}}'
exit 0
