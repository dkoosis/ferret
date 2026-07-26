#!/usr/bin/env bash
# feedback-stop.sh — the async half of the in-session feedback tap
# (ferret-wf9.1): on a turn that surfaced a settled kind:search retrieval
# event, fetch its returned nugs' bodies, run the content-relevance judge, and
# bank an ask on disagreement with the deterministic helped lattice.
#
# Registered as a Stop hook with async: true, asyncRewake: true (.claude/
# settings.json) — this script runs in the BACKGROUND, never blocking the
# turn it fired on. The ask it may bank never surfaces this turn; it surfaces
# on the next UserPromptSubmit (feedback-user-prompt-submit.sh), which is the
# entire point of the two-hook split (see the plan's Design section, "The
# Stop-hook / UserPromptSubmit split" — RunRelevance is a live paid API call
# that must never sit on a synchronous hook path).
#
# Exit code contract (asyncRewake): exit 0 for every outcome except a genuine
# pipeline failure (missing tool on PATH, every trixi-get 404ing, the judge
# erroring). A successful "nothing new this turn" and a successful "banked a
# candidate" are BOTH silent exit 0 — rewaking Claude to announce a bank would
# leak the ask into the same turn and defeat the design. Only a real failure
# exits 2, surfacing this script's stderr to Claude.

set -uo pipefail

fail() {
  echo "feedback-stop: $*" >&2
  exit 2
}

for bin in trixi jq ferret; do
  command -v "$bin" >/dev/null 2>&1 || fail "required tool '$bin' not found on PATH"
done

INPUT=$(cat)
SESSION_ID=$(jq -r '.session_id // empty' <<<"$INPUT" 2>/dev/null)
if [ -z "$SESSION_ID" ]; then
  # No session id on stdin — nothing we can scope prep/judge to. Not a
  # pipeline failure (this can legitimately happen on malformed hook input);
  # degrade quietly rather than asyncRewake-nagging over it.
  exit 0
fi

# Capture stdout ONLY — stderr flows through to this hook's own stderr
# untouched, not merged in. A merged 2>&1 would risk a coincidental stderr
# diagnostic (e.g. a corrupt-cursor self-heal warning) breaking the jq parse
# below, silently dropping an ask that prep already committed (cursor
# advanced, pending entry deleted) — a real, not hypothetical, failure mode.
if ! PREP_OUT=$(ferret feedback prep --session "$SESSION_ID"); then
  fail "feedback prep failed (see stderr above)"
fi

PENDING=$(jq -r '.pending // false' <<<"$PREP_OUT" 2>/dev/null)
if [ "$PENDING" != "true" ]; then
  exit 0 # nothing settled this turn — the common case, silent
fi

SEARCH_EVENT_ID=$(jq -r '.search_event_id // empty' <<<"$PREP_OUT")
if [ -z "$SEARCH_EVENT_ID" ]; then
  fail "feedback prep reported pending=true with no search_event_id: $PREP_OUT"
fi

# Loop trixi get over each returned nug id, assembling the [{id,text}]
# candidates judge reads on stdin. A single nug failing (404: pruned/
# consolidated between the search and this hook firing, but also possibly a
# systemic trixi problem) is logged — with trixi's own error text, so a
# systemic failure is diagnosable from this script's own stderr rather than
# reading as N separate unexplained 404s — and skipped, not fatal: one
# missing body must not kill the whole judge run. One temp file for the whole
# loop (not per-iteration) captures each attempt's stderr separately from its
# stdout (a plain 2>&1 merge risks corrupting the JSON payload if trixi ever
# logs a warning to stderr on an otherwise-successful call, as `trixi ask`
# does elsewhere); the trap cleans it up on any exit path, including `fail`.
TRIXI_ERR_FILE=$(mktemp)
trap 'rm -f "$TRIXI_ERR_FILE"' EXIT
CANDIDATES="[]"
FETCHED=0
while IFS= read -r NUG_ID; do
  [ -z "$NUG_ID" ] && continue
  if BODY_JSON=$(trixi get "$NUG_ID" --json 2>"$TRIXI_ERR_FILE"); then
    BODY=$(jq -r '.body // empty' <<<"$BODY_JSON")
    CANDIDATES=$(jq -c --arg id "$NUG_ID" --arg text "$BODY" '. + [{id:$id, text:$text}]' <<<"$CANDIDATES")
    FETCHED=$((FETCHED + 1))
  else
    TRIXI_ERR=$(cat "$TRIXI_ERR_FILE" 2>/dev/null)
    echo "feedback-stop: trixi get $NUG_ID failed — skipping: ${TRIXI_ERR:-no error detail}" >&2
  fi
done < <(jq -r '.nug_ids[]? // empty' <<<"$PREP_OUT")

if [ "$FETCHED" -eq 0 ]; then
  # Every returned nug failed to fetch — the common, benign case is ordinary
  # settled-tail delay (nugs can get pruned/consolidated between the search
  # and judge finally running); NOT by itself evidence of a systemic trixi
  # problem, which would already have been diagnosable per-nug above. Exit
  # quietly rather than asyncRewake-waking Claude over routine nug churn.
  exit 0
fi

# A here-string (<<<), not `echo "$CANDIDATES" | ...`: some shells' echo
# builtin (confirmed: zsh's default echo) interprets backslash escapes in its
# argument, turning jq's escaped "\n" (two literal chars, backslash+n) inside
# a candidate body into an actual newline byte — silently corrupting the JSON
# into something jq/ferret then reject as "control characters ... must be
# escaped". A here-string passes the variable's bytes verbatim. Caught live in
# this bead's Step 18 dry run against a real multi-line nug body.
JUDGE_OUT=$(ferret feedback judge --session "$SESSION_ID" --search-event "$SEARCH_EVENT_ID" <<<"$CANDIDATES" 2>&1) \
  || fail "feedback judge failed: $JUDGE_OUT"

exit 0
