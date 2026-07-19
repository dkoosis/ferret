package analyst

import (
	"errors"
	"strings"
	"testing"

	"github.com/dkoosis/keyring"
)

func TestBuildPromptEmbedsSpineAndSchema(t *testing.T) {
	system, user := BuildPrompt("spine session=abc\n[user] do a thing\n[call] Bash command=rg foo")
	if !strings.Contains(system, "served its purpose") || !strings.Contains(system, "snipe") {
		t.Errorf("system prompt missing framing: %q", system)
	}
	if !strings.Contains(user, "spine session=abc") {
		t.Errorf("user prompt missing spine: %q", user)
	}
	if !strings.Contains(user, `"fit":"served|mismatch"`) {
		t.Errorf("user prompt missing schema instruction: %q", user)
	}
}

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name    string
		resp    string
		want    int
		wantErr bool
	}{
		{
			name: "bare json",
			resp: `{"findings":[{"task":"find def","call":"rg func X","toolUsed":"rg","fit":"mismatch","better":"snipe","why":"symbol lookup","confidence":"high"}]}`,
			want: 1,
		},
		{
			name: "fenced json with prose",
			resp: "Here are the findings:\n```json\n{\"findings\":[{\"task\":\"t\",\"fit\":\"served\"}]}\n```\n",
			want: 1,
		},
		{
			name: "empty findings",
			resp: `{"findings":[]}`,
			want: 0,
		},
		{
			// ferret-001: valid JSON then trailing prose containing a stray '}'.
			// The old first-'{'…last-'}' span grabbed the brace in "`gofmt {}`",
			// over-captured, and failed the whole parse — discarding a paid call.
			name: "trailing prose with stray brace",
			resp: "{\"findings\":[{\"task\":\"t\",\"fit\":\"served\"}]}\nNote: run `gofmt {}` to format.",
			want: 1,
		},
		{
			name:    "no json",
			resp:    "I could not find any tool calls.",
			wantErr: true,
		},
		{
			name:    "malformed json",
			resp:    `{"findings":[{"task":}}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFindings(tt.resp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFindings() err = nil; want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFindings() err = %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("ParseFindings() = %d findings; want %d", len(got), tt.want)
			}
		})
	}
}

func TestParseFindingsNoJSONIsTyped(t *testing.T) {
	if _, err := ParseFindings("no json here"); !errors.Is(err, errNoJSON) {
		t.Errorf("err = %v; want errNoJSON", err)
	}
}

// TestParseFindingsTruncatedIsTyped: a response cut mid-array (the max_tokens
// under-capture case, ferret-e6s) must decode to the typed ErrTruncatedResponse,
// not a bare io.ErrUnexpectedEOF — the paid call hit the output cap, and the
// operator needs that named so they can split the session and re-run.
func TestParseFindingsTruncatedIsTyped(t *testing.T) {
	// Well-formed JSON object opened, one finding started, then cut off.
	truncated := `{"findings":[{"task":"find def","call":"rg func X","fit":"mismatch","why":"symbol look`
	if _, err := ParseFindings(truncated); !errors.Is(err, ErrTruncatedResponse) {
		t.Errorf("err = %v; want ErrTruncatedResponse", err)
	}
	// Proposals and relevance share decodeFirstObject, so they inherit the guard.
	if _, err := ParseProposals(truncated); !errors.Is(err, ErrTruncatedResponse) {
		t.Errorf("ParseProposals err = %v; want ErrTruncatedResponse", err)
	}
}

func TestResultMismatches(t *testing.T) {
	r := Result{Findings: []Finding{
		{Fit: FitMismatch, ToolUsed: "rg", Better: "snipe"},
		{Fit: FitServed, ToolUsed: "rg"},
		{Fit: FitMismatch, ToolUsed: "grep", Better: "snipe"},
	}}
	got := r.Mismatches()
	if len(got) != 2 {
		t.Fatalf("Mismatches() = %d; want 2", len(got))
	}
	for _, f := range got {
		if f.Fit != FitMismatch {
			t.Errorf("Mismatches() included fit=%q", f.Fit)
		}
	}
}

func TestConfigModelDefault(t *testing.T) {
	if got := (Config{}).model(); got != DefaultModel {
		t.Errorf("empty Config model() = %q; want %q", got, DefaultModel)
	}
	if got := (Config{Model: "claude-opus-4-8"}).model(); got != "claude-opus-4-8" {
		t.Errorf("model() = %q; want override", got)
	}
}

func TestConfigHasAPIKey(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	if ok, err := (Config{}).HasAPIKey(); ok || err != nil {
		t.Errorf("HasAPIKey() = (%v, %v) with no key set", ok, err)
	}
	if ok, err := (Config{APIKey: "sk-x"}).HasAPIKey(); !ok || err != nil {
		t.Errorf("HasAPIKey() = (%v, %v) with explicit key", ok, err)
	}
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "sk-env")
	if ok, err := (Config{}).HasAPIKey(); !ok || err != nil {
		t.Errorf("HasAPIKey() = (%v, %v) with env key", ok, err)
	}
}

func TestConfigHasAPIKey_UnreadableKeychainSurfaces(t *testing.T) {
	restore := readKeychain
	defer func() { readKeychain = restore }()
	readKeychain = func() (string, error) { return "", keyring.ErrUnreadable }
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "sk-env")
	if _, err := (Config{}).HasAPIKey(); !errors.Is(err, keyring.ErrUnreadable) {
		t.Errorf("locked keychain must surface through HasAPIKey, got %v", err)
	}
}

func TestRunWithoutKeyReturnsErrNoAPIKey(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	if _, err := Run(t.Context(), Config{}, "sess", "spine"); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("Run() err = %v; want ErrNoAPIKey", err)
	}
}
