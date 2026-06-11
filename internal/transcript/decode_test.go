package transcript_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/dkoosis/ferret/internal/transcript"
)

type readLinesInput struct {
	contents       string
	missing        bool
	failAfterLines int
}

type lineRecord struct {
	Text       string
	Len        int
	HasNewline bool
}

var errReadLinesCallback = errors.New("read lines callback failed")

func TestReadLines_DeliversTranscriptLines_When_FileIsReadable(t *testing.T) {
	t.Parallel()

	longLine := strings.Repeat("x", 1<<20+17) + "\n"

	tests := []struct {
		name    string
		input   readLinesInput
		want    []lineRecord
		wantErr error
		inspect func(*testing.T, []lineRecord)
	}{
		{
			name: "errors when transcript file does not exist",
			input: readLinesInput{
				missing: true,
			},
			wantErr: os.ErrNotExist,
		},
		{
			name: "returns callback error and stops reading subsequent lines",
			input: readLinesInput{
				contents:       "first\nsecond\n",
				failAfterLines: 1,
			},
			wantErr: errReadLinesCallback,
		},
		{
			name: "empty transcript produces no callbacks",
			input: readLinesInput{
				contents: "",
			},
			inspect: func(t *testing.T, got []lineRecord) {
				t.Helper()
				require.Empty(t, got, "empty input must not synthesize transcript lines")
			},
		},
		{
			name: "final line without newline is still delivered",
			input: readLinesInput{
				contents: "unterminated final event",
			},
			want: []lineRecord{
				{Text: "unterminated final event", Len: len("unterminated final event"), HasNewline: false},
			},
			inspect: func(t *testing.T, got []lineRecord) {
				t.Helper()
				require.Len(t, got, 1)
				require.False(t, got[0].HasNewline, "ReadLines must not append bytes that were not present")
			},
		},
		{
			name: "line larger than scanner token limit is delivered whole",
			input: readLinesInput{
				contents: longLine,
			},
			want: []lineRecord{
				{Len: len(longLine), HasNewline: true},
			},
			inspect: func(t *testing.T, got []lineRecord) {
				t.Helper()
				require.Len(t, got, 1)
				require.Greater(t, got[0].Len, 1<<20, "large tool-result lines must survive ingestion")
				require.True(t, got[0].HasNewline, "line terminator is part of the delivered bytes")
			},
		},
		{
			name: "multiple newline terminated transcript lines keep order and delimiters",
			input: readLinesInput{
				contents: "alpha\nbeta\ngamma\n",
			},
			want: []lineRecord{
				{Text: "alpha\n", Len: len("alpha\n"), HasNewline: true},
				{Text: "beta\n", Len: len("beta\n"), HasNewline: true},
				{Text: "gamma\n", Len: len("gamma\n"), HasNewline: true},
			},
			inspect: func(t *testing.T, got []lineRecord) {
				t.Helper()
				require.Len(t, got, 3)
				for _, line := range got {
					require.True(t, line.HasNewline, "newline-terminated input lines must keep their delimiter")
					require.Equal(t, len(line.Text), line.Len, "reported length must match delivered bytes")
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if !tc.input.missing {
				require.NoError(t, os.WriteFile(path, []byte(tc.input.contents), 0o600))
			}

			var got []lineRecord
			seen := 0
			err := transcript.ReadLines(path, func(line []byte) error {
				seen++
				got = append(got, recordLine(line, tc.input.contents))
				if tc.input.failAfterLines > 0 && seen >= tc.input.failAfterLines {
					return errReadLinesCallback
				}
				return nil
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("diff (-want +got):\n%s", diff)
			}

			if tc.inspect != nil {
				tc.inspect(t, got)
			}
		})
	}
}

func recordLine(line []byte, source string) lineRecord {
	text := string(line)
	rec := lineRecord{
		Len:        len(line),
		HasNewline: strings.HasSuffix(text, "\n"),
	}
	if len(source) <= 4096 {
		rec.Text = text
	}
	return rec
}
