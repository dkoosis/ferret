package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIsMutatingTool(t *testing.T) {
	for _, name := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit"} {
		if !isMutatingTool(name) {
			t.Errorf("isMutatingTool(%q) = false, want true", name)
		}
	}
	// Read-capable / read-only built-ins are excluded from the v1 gate — Bash most
	// deliberately (it would flood the judge with ls/cat/rg).
	for _, name := range []string{"Read", "Bash", "Grep", "Glob", "WebFetch"} {
		if isMutatingTool(name) {
			t.Errorf("isMutatingTool(%q) = true, want false", name)
		}
	}
}

func TestActionDetail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"file_path", `{"file_path":"internal/score/reach.go","old_string":"x"}`, "internal/score/reach.go"},
		{"notebook_path", `{"notebook_path":"nb.ipynb"}`, "nb.ipynb"},
		{"neither", `{"command":"rm -rf /"}`, ""},
		{"empty", ``, ""},
		{"undecodable", `{not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actionDetail(json.RawMessage(tt.input)); got != tt.want {
				t.Errorf("actionDetail(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsPushbackTurn(t *testing.T) {
	// A repair/reject move and an explicit over-initiative reversal are the floor's
	// job — this leg must skip them.
	for _, turn := range []string{
		"no, that's not what I wanted",
		"I didn't ask you to change that",
		"who told you to delete it",
	} {
		if !isPushbackTurn(turn) {
			t.Errorf("isPushbackTurn(%q) = false, want true (floor owns it)", turn)
		}
	}
	// A neutral follow-on or an acceptance is NOT pushback — the action stood, so the
	// episode is a no-pushback candidate.
	for _, turn := range []string{
		"now add a test for it",
		"thanks, looks good",
	} {
		if isPushbackTurn(turn) {
			t.Errorf("isPushbackTurn(%q) = true, want false", turn)
		}
	}
}

// TestCollectCandidates_NoPushbackMutation is the core gate: an advice-scoped prompt
// followed by a mutating agent action and a NON-pushback human turn is a candidate;
// the same shape where the human DID push back is not (the floor owns it).
func TestCollectCandidates_NoPushbackMutation(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-oi.jsonl", []string{
		// advice-scoped opening prompt
		`{"type":"user","sessionId":"sess-oi","message":{"role":"user","content":"what are my options for the retry backoff?"}}`,
		// agent mutates instead of advising
		`{"type":"assistant","sessionId":"sess-oi","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"client.go","old_string":"a","new_string":"b"}}]}}`,
		// human lets it stand (neutral follow-on, not a repair) — closes the window as a candidate
		`{"type":"user","sessionId":"sess-oi","message":{"role":"user","content":"ok now write the changelog"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-oi.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 no-pushback candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].Prompt != "what are my options for the retry backoff?" {
		t.Errorf("candidate prompt = %q", cands[0].Prompt)
	}
	if len(cands[0].Actions) != 1 || cands[0].Actions[0].Tool != "Edit" || cands[0].Actions[0].Detail != "client.go" {
		t.Errorf("candidate actions = %+v", cands[0].Actions)
	}
}

// TestCollectCandidates_PushbackSkipped confirms the floor's case is excluded: when
// the human reverses the action, this leg emits nothing for that window.
func TestCollectCandidates_PushbackSkipped(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-pb.jsonl", []string{
		`{"type":"user","sessionId":"sess-pb","message":{"role":"user","content":"should we rename the field?"}}`,
		`{"type":"assistant","sessionId":"sess-pb","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"schema.go"}}]}}`,
		// explicit reversal → the deterministic floor owns it → no LLM candidate here
		`{"type":"user","sessionId":"sess-pb","message":{"role":"user","content":"I didn't ask you to change the schema"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-pb.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (floor owns the pushback case), got %d: %+v", len(cands), cands)
	}
}

// TestCollectCandidates_TrailingWindow confirms the last window — a mutation with no
// following human turn — is a candidate (the human let it stand by never reacting).
func TestCollectCandidates_TrailingWindow(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-tr.jsonl", []string{
		`{"type":"user","sessionId":"sess-tr","message":{"role":"user","content":"can you review this function?"}}`,
		`{"type":"assistant","sessionId":"sess-tr","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"reviewed.go"}}]}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-tr.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 trailing candidate, got %d", len(cands))
	}
}

// TestCollectCandidates_ReadOnlyNoCandidate confirms an episode with no mutating
// action is never a candidate — a read-only agent turn cannot be over-initiative.
func TestCollectCandidates_ReadOnlyNoCandidate(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-ro.jsonl", []string{
		`{"type":"user","sessionId":"sess-ro","message":{"role":"user","content":"what does this file do?"}}`,
		`{"type":"assistant","sessionId":"sess-ro","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x.go"}}]}}`,
		`{"type":"user","sessionId":"sess-ro","message":{"role":"user","content":"got it, thanks"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-ro.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (no mutation), got %d", len(cands))
	}
}

// TestCollectCandidates_ErroredMutationExcluded is the core AC (ferret-xg4): a
// mutating tool_use whose tool_result reports is_error MUST NOT be recorded as an
// AgentAction. The window has exactly one mutation and it failed, so the episode
// has zero landed actions and is not a candidate at all.
func TestCollectCandidates_ErroredMutationExcluded(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-err.jsonl", []string{
		`{"type":"user","sessionId":"sess-err","message":{"role":"user","content":"what are my options for the retry backoff?"}}`,
		`{"type":"assistant","sessionId":"sess-err","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"client.go"}}]}}`,
		// the edit failed (or the user rejected the permission prompt — CC surfaces
		// both as is_error on the tool_result) — the mutation never landed
		`{"type":"user","sessionId":"sess-err","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"edit failed: no such file"}]}}`,
		`{"type":"user","sessionId":"sess-err","message":{"role":"user","content":"ok now write the changelog"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-err.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (only mutation errored, never landed), got %d: %+v", len(cands), cands)
	}
}

// TestCollectCandidates_RejectedMutationExcludedAmongLanded confirms selective
// exclusion: of two mutations in one window, only the one whose tool_result is a
// user rejection (is_error) is dropped — the other, successfully-landed mutation
// still reaches the judge.
func TestCollectCandidates_RejectedMutationExcludedAmongLanded(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-rej.jsonl", []string{
		`{"type":"user","sessionId":"sess-rej","message":{"role":"user","content":"what are my options here?"}}`,
		`{"type":"assistant","sessionId":"sess-rej","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"rejected.go"}}]}}`,
		// the user rejected this edit via the permission prompt — CC records it as
		// an errored tool_result on the same tool_use_id
		`{"type":"user","sessionId":"sess-rej","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"The user doesn't want to proceed with this tool use."}]}}`,
		`{"type":"assistant","sessionId":"sess-rej","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"landed.go"}}]}}`,
		`{"type":"user","sessionId":"sess-rej","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t2","is_error":false,"content":"ok"}]}}`,
		`{"type":"user","sessionId":"sess-rej","message":{"role":"user","content":"thanks, that's fine"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-rej.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate (the landed Write survives), got %d: %+v", len(cands), cands)
	}
	if len(cands[0].Actions) != 1 || cands[0].Actions[0].Tool != "Write" || cands[0].Actions[0].Detail != "landed.go" {
		t.Errorf("candidate actions = %+v, want only the landed Write", cands[0].Actions)
	}
}

// TestCollectCandidates_AffirmationClosesWindow confirms a folded affirmation
// between the prompt and the next genuine turn does NOT emit a candidate — the human
// engaged ("go ahead" authorizes), so the window closes without flagging.
func TestCollectCandidates_AffirmationClosesWindow(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-af.jsonl", []string{
		`{"type":"user","sessionId":"sess-af","message":{"role":"user","content":"what are my options here?"}}`,
		`{"type":"assistant","sessionId":"sess-af","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"a.go"}}]}}`,
		// a bare affirmation folds — authorizes rather than reverses
		`{"type":"user","sessionId":"sess-af","message":{"role":"user","content":"go ahead"}}`,
	})
	cands, _, err := collectOverInitiativeCandidates(filepath.Join(root, "-Users-dev-proj", "sess-af.jsonl"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (affirmation authorizes), got %d: %+v", len(cands), cands)
	}
}
