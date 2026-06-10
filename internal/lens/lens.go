// Package lens maps canonical events to algorithm-facing tokens.
// Tokenization is the product: each lens is a different granularity,
// and different lenses surface different patterns.
package lens

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// Lens turns an event into a token. ok=false means the event is
// invisible to this lens.
type Lens interface {
	Name() string
	Token(e *event.Event) (tok string, ok bool)
}

var registry = map[string]Lens{}

func register(l Lens) { registry[l.Name()] = l }

func Get(name string) (Lens, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown lens %q (have: %s)", name, strings.Join(Names(), ", "))
	}
	return l, nil
}

func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// mcpShort compresses mcp__server__tool action names to mcp:server.tool.
func mcpShort(action string) string {
	parts := strings.SplitN(strings.TrimPrefix(action, "mcp__"), "__", 2)
	if len(parts) == 2 {
		return "mcp:" + parts[0] + "." + parts[1]
	}
	return "mcp:" + parts[0]
}

func isMCP(action string) bool { return strings.HasPrefix(action, "mcp__") }
