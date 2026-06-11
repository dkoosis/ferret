package sweagent

import (
	"encoding/json"
	"strings"
)

// Row is one dataset row = one trajectory = one stream.
//
// The HF card does not pin field names, so decoding is tolerant:
//   - instance_id: instance_id | instance | id
//   - target: target (bool) | resolved (bool)
//   - exit_status: exit_status | exit
//   - trajectory: trajectory | messages | history (a JSON array of messages,
//     or a JSON-encoded string holding that array — the duckdb export of a
//     struct column can produce either)
type Row struct {
	InstanceID string
	Target     bool
	ExitStatus string
	Trajectory []Message
}

// rawRow mirrors the wire shape before normalization. Trajectory is
// json.RawMessage because it may be an array or a quoted string.
type rawRow struct {
	InstanceID  string          `json:"instance_id"`
	InstanceAlt string          `json:"instance"`
	IDAlt       string          `json:"id"`
	Target      *bool           `json:"target"`
	Resolved    *bool           `json:"resolved"`
	ExitStatus  string          `json:"exit_status"`
	ExitAlt     string          `json:"exit"`
	Trajectory  json.RawMessage `json:"trajectory"`
	Messages    json.RawMessage `json:"messages"`
	History     json.RawMessage `json:"history"`
}

// Message is one role-tagged trajectory step. Actions live on ai/assistant
// messages; observations on the following user message.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string, or a content-block array
	Action  string          `json:"action"`  // SWE-agent logs the raw action here
}

func (m Message) isAI() bool {
	r := strings.ToLower(m.Role)
	return r == "ai" || r == "assistant"
}

func (m Message) isUser() bool {
	r := strings.ToLower(m.Role)
	return r == "user" || r == "tool" || r == "observation"
}

// action returns the action string for an ai message: the explicit action
// field when present, else the message content (some exports inline the
// action as the assistant's content).
func (m Message) action() string {
	if strings.TrimSpace(m.Action) != "" {
		return m.Action
	}
	return m.content()
}

// content flattens Content, which may be a bare string or a list of
// {type,text} blocks.
func (m Message) content() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(m.Content, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
			b.WriteString(blk.Content)
		}
		return b.String()
	}
	return ""
}

// DecodeRow parses one JSONL line into a normalized Row.
func DecodeRow(line []byte) (*Row, error) {
	var rr rawRow
	if err := json.Unmarshal(line, &rr); err != nil {
		return nil, err
	}
	r := &Row{
		InstanceID: firstNonEmpty(rr.InstanceID, rr.InstanceAlt, rr.IDAlt),
		ExitStatus: firstNonEmpty(rr.ExitStatus, rr.ExitAlt),
	}
	switch {
	case rr.Target != nil:
		r.Target = *rr.Target
	case rr.Resolved != nil:
		r.Target = *rr.Resolved
	}
	traj := firstNonEmptyRaw(rr.Trajectory, rr.Messages, rr.History)
	msgs, err := decodeTrajectory(traj)
	if err != nil {
		return nil, err
	}
	r.Trajectory = msgs
	return r, nil
}

// decodeTrajectory accepts either a JSON array of messages or a JSON string
// that itself encodes that array (struct-column exports vary).
func decodeTrajectory(raw json.RawMessage) ([]Message, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err == nil {
		return msgs, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(s), &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyRaw(vals ...json.RawMessage) json.RawMessage {
	for _, v := range vals {
		if len(v) > 0 && string(v) != "null" {
			return v
		}
	}
	return nil
}
