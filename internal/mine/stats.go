package mine

import (
	"sort"

	"github.com/dkoosis/ferret/internal/event"
)

// Bucket is one aggregation row in a summary.
type Bucket struct {
	Key      string         `json:"key"`
	Events   int            `json:"events"`
	Sessions int            `json:"sessions,omitempty"`
	Fails    int            `json:"fails"`
	Retries  int            `json:"retries"`
	Unpaired int            `json:"unpaired"`
	ByKind   map[string]int `json:"byKind,omitempty"`

	sessions map[string]struct{}
}

// TopAction is an action with its count and failure count.
type TopAction struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
	Fails  int    `json:"fails"`
}

// Summary aggregates the events artifact at corpus/project/session grain.
type Summary struct {
	By         string      `json:"by"`
	Buckets    []*Bucket   `json:"buckets"`
	TopActions []TopAction `json:"topActions,omitempty"` // corpus grain only
}

// Summarize streams the artifact once. by ∈ corpus|project|session.
func Summarize(eventsPath, by string) (*Summary, error) {
	buckets := map[string]*Bucket{}
	actions := map[string]*TopAction{}

	err := event.Read(eventsPath, func(ev *event.Event) error {
		var key string
		switch by {
		case "project":
			key = ev.Project
		case "session":
			key = ev.Project + "/" + ev.Session
		default:
			key = "corpus"
		}
		b, ok := buckets[key]
		if !ok {
			b = &Bucket{Key: key, ByKind: map[string]int{}, sessions: map[string]struct{}{}}
			buckets[key] = b
		}
		b.Events++
		b.ByKind[ev.Kind]++
		b.sessions[ev.Session] = struct{}{}
		if ev.Status == event.StatusFail {
			b.Fails++
		}
		if ev.Status == event.StatusNone && ev.Kind != event.KindPrompt {
			b.Unpaired++
		}
		if ev.Retry {
			b.Retries++
		}
		if ev.Kind != event.KindPrompt {
			name := ev.Action
			if ev.Kind == event.KindShell {
				name = "sh:" + name
			}
			a, ok := actions[name]
			if !ok {
				a = &TopAction{Action: name}
				actions[name] = a
			}
			a.Count++
			if ev.Status == event.StatusFail {
				a.Fails++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s := &Summary{By: by}
	for _, b := range buckets {
		b.Sessions = len(b.sessions)
		s.Buckets = append(s.Buckets, b)
	}
	sort.Slice(s.Buckets, func(i, j int) bool { return s.Buckets[i].Events > s.Buckets[j].Events })
	for _, a := range actions {
		s.TopActions = append(s.TopActions, *a)
	}
	sort.Slice(s.TopActions, func(i, j int) bool { return s.TopActions[i].Count > s.TopActions[j].Count })
	return s, nil
}
