package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// Probe is the cheap first-pass decode: only the event type.
type Probe struct {
	Type string `json:"type"`
}

// Raw is the full decode for event types we care about (assistant/user/
// attachment). Every field is optional — the schema drifts across CC versions.
type Raw struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	UUID        string `json:"uuid"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Version     string `json:"version"`
	Skill       string `json:"attributionSkill"`
	Plugin      string `json:"attributionPlugin"`
	MCPServer   string `json:"attributionMcpServer"`
	Message     *Msg   `json:"message"`
	// Attachment is the harness-injected payload on a "attachment" line. Kept
	// RAW, not decoded into fields, because the classes have no common content
	// key: hook_success carries content/stdout/stderr, skill_listing content,
	// edited_text_file snippet, output_style style, diagnostics files,
	// queued_command prompt, the *_delta classes addedLines/addedBlocks. An
	// allowlist of per-class keys would silently score every future class at
	// zero — which is precisely how ~235MB stayed invisible. Measuring the
	// serialized record needs no per-class knowledge and cannot regress that way.
	Attachment json.RawMessage `json:"attachment"`
}

// AttachClass is the cheap decode of an attachment's own discriminator. It is
// the only field ferret reads out of the payload; the rest is weighed, not read.
type AttachClass struct {
	Type string `json:"type"`
}

type Msg struct {
	Role    string `json:"role"`
	Content Blocks `json:"content"`
}

// Blocks tolerates both string content and []Block content.
type Blocks []Block

func (b *Blocks) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*b = Blocks{{Type: "text", Text: s}}
		return nil
	}
	var a []Block
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*b = Blocks(a)
	return nil
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"` // body of a "thinking" block (CC keys it separately from text)
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   *bool           `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result payload — measured for burn (string or block array)
}

// ReadLines streams a transcript line by line. No Scanner token limit —
// tool results with embedded images can run to megabytes. A decode-broken
// or truncated final line is the caller's problem; ReadLines just delivers bytes.
func ReadLines(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if ferr := fn(line); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
