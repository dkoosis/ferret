package analyst

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildProposePromptEmbedsBundleSpineAndSchema(t *testing.T) {
	system, user := BuildProposePrompt(
		`{"candidates":[{"task":1,"outWeight":0.73}]}`,
		"spine session=abc\n[user] do the read pass\n[call] Read internal/x.go",
	)
	if !strings.Contains(system, "automate") || !strings.Contains(system, "de-context") {
		t.Errorf("system prompt missing both levers: %q", system)
	}
	if !strings.Contains(user, `"task":1`) {
		t.Errorf("user prompt missing candidate bundle: %q", user)
	}
	if !strings.Contains(user, "spine session=abc") {
		t.Errorf("user prompt missing spine: %q", user)
	}
	if !strings.Contains(user, `"kind":"automate|de-context|none"`) {
		t.Errorf("user prompt missing schema instruction: %q", user)
	}
}

func TestParseProposals(t *testing.T) {
	tests := []struct {
		name    string
		resp    string
		want    int
		wantErr bool
	}{
		{
			name: "bare json",
			resp: `{"proposals":[{"task":4,"kind":"de-context","proposal":"replace full-file Reads with snipe pack","why":"ow=0.69, 22KB output","confidence":"high"}]}`,
			want: 1,
		},
		{
			name: "fenced json with prose",
			resp: "Proposals:\n```json\n{\"proposals\":[{\"task\":1,\"kind\":\"automate\"},{\"task\":2,\"kind\":\"none\"}]}\n```\n",
			want: 2,
		},
		{
			name: "empty proposals",
			resp: `{"proposals":[]}`,
			want: 0,
		},
		{
			name:    "no json",
			resp:    "I could not find any candidates.",
			wantErr: true,
		},
		{
			name:    "malformed json",
			resp:    `{"proposals":[{"task":}}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProposals(tt.resp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseProposals() err = nil; want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProposals() err = %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("ParseProposals() = %d proposals; want %d", len(got), tt.want)
			}
		})
	}
}

func TestParseProposalsNoJSONIsTyped(t *testing.T) {
	if _, err := ParseProposals("no json here"); !errors.Is(err, errNoJSON) {
		t.Errorf("err = %v; want errNoJSON", err)
	}
}

func TestProposeResultActionable(t *testing.T) {
	r := ProposeResult{Proposals: []Proposal{
		{Task: 1, Kind: ProposeAutomate},
		{Task: 2, Kind: ProposeNone},
		{Task: 3, Kind: ProposeDeContext},
	}}
	got := r.Actionable()
	if len(got) != 2 {
		t.Fatalf("Actionable() = %d; want 2", len(got))
	}
	for _, p := range got {
		if p.Kind == ProposeNone {
			t.Errorf("Actionable() included kind=none (task %d)", p.Task)
		}
	}
}

func TestRunProposeWithoutKeyReturnsErrNoAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := RunPropose(t.Context(), Config{}, "sess", "{}", "spine"); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("RunPropose() err = %v; want ErrNoAPIKey", err)
	}
}
