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
	// ID is the API response id. It is NOT unique per line: one response is
	// written across several assistant lines (one per content block), each
	// repeating the SAME usage. Measured on a live transcript: 94 usage-bearing
	// lines carried only 61 distinct ids, with every repeat byte-identical.
	// Summing per line therefore over-counts spend by ~54%, and the message id
	// is the only key that collapses it — the per-line uuid differs.
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *Usage `json:"usage"`
}

// Usage is the API's own token ledger for one call — the harness wrote it, so
// it is measured spend rather than anything ferret inferred.
type Usage struct {
	Input      int `json:"input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Output     int `json:"output_tokens"`
	Details    *struct {
		Thinking int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	// CacheCreation splits the write by TTL. A 5-minute write that expires
	// before its next read is a full-price write that bought nothing.
	CacheCreation *struct {
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
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
