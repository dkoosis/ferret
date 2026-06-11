package transcript_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

var errCallback = errors.New("read lines callback failed")

// collectLines drives ReadLines and records every delivered line verbatim.
// failAfter > 0 makes the callback fail once it has seen that many lines, so
// callers can assert the stream stops at exactly that point.
func collectLines(path string, failAfter int) ([]string, error) {
	var got []string
	err := transcript.ReadLines(path, func(line []byte) error {
		got = append(got, string(line))
		if failAfter > 0 && len(got) >= failAfter {
			return errCallback
		}
		return nil
	})
	return got, err
}

// assertLines compares without ever dumping line contents — a failing
// large-line case must not splat a megabyte into the test log.
func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("delivered %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %d bytes, want %d bytes", i, len(got[i]), len(want[i]))
		}
	}
}

func TestReadLines_DeliversBytesVerbatim_When_FileIsReadable(t *testing.T) {
	t.Parallel()

	// One past the 1<<20 reader buffer: a single ReadBytes call must span fills.
	longLine := strings.Repeat("x", 1<<20+17) + "\n"

	tests := []struct {
		name      string
		contents  string
		missing   bool
		failAfter int
		want      []string // delivered lines on the success path
		wantErr   error
		wantSeen  int // lines delivered before the error fires
	}{
		{
			name:    "missing file surfaces the open error",
			missing: true,
			wantErr: os.ErrNotExist,
		},
		{
			name:      "callback error stops the stream",
			contents:  "first\nsecond\nthird\n",
			failAfter: 1,
			wantErr:   errCallback,
			wantSeen:  1, // invariant: nothing delivered after the failing callback
		},
		{
			name:     "empty file yields no callbacks",
			contents: "",
			want:     nil,
		},
		{
			name:     "unterminated final line is delivered verbatim, no added newline",
			contents: "no trailing newline",
			want:     []string{"no trailing newline"},
		},
		{
			name:     "line larger than the read buffer survives whole",
			contents: longLine,
			want:     []string{longLine},
		},
		{
			name:     "multiple lines keep order and delimiters",
			contents: "alpha\nbeta\ngamma\n",
			want:     []string{"alpha\n", "beta\n", "gamma\n"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}

			got, err := collectLines(path, tc.failAfter)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(got) != tc.wantSeen {
					t.Errorf("delivered %d lines before error, want %d", len(got), tc.wantSeen)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertLines(t, got, tc.want)

			// Invariant: delivery is lossless — concatenated lines equal the file.
			if joined := strings.Join(got, ""); joined != tc.contents {
				t.Errorf("reassembled %d bytes, want %d (delivery must be lossless)", len(joined), len(tc.contents))
			}
		})
	}
}

// Blocks.UnmarshalJSON is the schema-drift tolerance point: CC emits message
// content as either a bare string or an array of typed blocks. Both must decode.
func TestBlocks_UnmarshalJSON_ToleratesStringAndArrayContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    transcript.Blocks
		wantErr bool
	}{
		{
			name: "string content collapses to a single text block",
			json: `{"role":"user","content":"hello world"}`,
			want: transcript.Blocks{{Type: "text", Text: "hello world"}},
		},
		{
			name: "array content preserves typed blocks in order",
			json: `{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t1","name":"Bash"}]}`,
			want: transcript.Blocks{
				{Type: "text", Text: "hi"},
				{Type: "tool_use", ID: "t1", Name: "Bash"},
			},
		},
		{
			name: "empty array yields zero blocks",
			json: `{"role":"user","content":[]}`,
			want: transcript.Blocks{},
		},
		{
			name:    "non-string non-array content is an error",
			json:    `{"role":"user","content":123}`,
			wantErr: true,
		},
		{
			name:    "malformed string content is an error",
			json:    `{"role":"user","content":"\uZZZZ"}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var msg transcript.Msg
			err := json.Unmarshal([]byte(tc.json), &msg)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("decoded %q without error, want failure", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(msg.Content, tc.want) {
				t.Errorf("content = %+v, want %+v", msg.Content, tc.want)
			}
		})
	}
}
