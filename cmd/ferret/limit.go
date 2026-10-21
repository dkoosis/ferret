package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/kong"
)

// limitAll is the value --limit all decodes to. Any negative number means
// "no cap" to applyDefaultLimit; -1 is the one the help text has always named.
const limitAll = -1

var errBadLimit = usage("bad --limit (want all or an integer; --limit=-1 also means all)")

// Limit is the --limit flag on every command that embeds CommonFlags. It is an
// int underneath, so applyDefaultLimit and the renderers see the numbers they
// always saw; the type exists only to let the flag read the word "all".
//
// kong reads `--limit -1` as a short flag, so only `--limit=-1` could spell
// "unlimited". `--limit all` spells it without the trap (ferret-38r).
type Limit int

// Decode implements kong.MapperValue: "all" becomes limitAll, an integer
// stays itself, and anything else is an error naming both accepted forms.
func (l *Limit) Decode(ctx *kong.DecodeContext) error {
	var raw string
	if err := ctx.Scan.PopValueInto("limit", &raw); err != nil {
		return err
	}
	v, err := parseLimit(raw)
	if err != nil {
		return err
	}
	*l = v
	return nil
}

func parseLimit(raw string) (Limit, error) {
	if strings.EqualFold(raw, "all") {
		return limitAll, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", errBadLimit, raw)
	}
	return Limit(n), nil
}
