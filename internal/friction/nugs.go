package friction

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// NugKind is the mnemd nug kind that records a friction: a trap a past session
// hit, written at /wrap. It is the only kind this reader admits.
const NugKind = "friction"

// Nug is the slice of a friction nug the seeder needs: its id (the join key a
// downstream prompt can open), where it lives, and its body text.
type Nug struct {
	ID   string
	Path string
	Body string
}

// ReadFrictionNugs walks a mnemd nugbase (a markdown tree whose files carry a
// YAML frontmatter block) and returns every nug whose frontmatter kind is
// friction, sorted by path for deterministic output. Dot-directories (.mnemd,
// .obsidian, .git) are skipped: they hold the store's own cache and journals,
// never nugs. A file that is not a nug (no frontmatter, another kind) is simply
// not one; only an unreadable tree or file is an error.
//
// The frontmatter is scanned as flat `key: value` lines rather than parsed as
// YAML: mnemd writes it, the two keys read here (id, kind) are single-line
// scalars, and a YAML dependency for two keys is more surface than the format
// warrants. A nested or multi-line value for either key would be a schema
// change this reader should fail loudly on, and it does (no id → skipped with
// the kind still matched is impossible, since id is a MUST field in mnemd's
// schema).
func ReadFrictionNugs(root string) ([]Nug, error) {
	var out []Nug
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		n, ok, err := readNug(path)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// readNug parses one markdown file. ok is false when the file is not a friction
// nug (no leading frontmatter fence, or a kind other than friction).
func readNug(path string) (n Nug, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return Nug{}, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Nug{}, false, nil // not a nug: no frontmatter
	}
	var id, kind string
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(k) {
		case "id":
			id = unquote(v)
		case "kind":
			kind = unquote(v)
		}
	}
	if !closed || kind != NugKind {
		return Nug{}, false, sc.Err()
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return Nug{}, false, fmt.Errorf("friction: %s: %w", path, err)
	}
	if id == "" {
		return Nug{}, false, fmt.Errorf("friction: %s: %w", path, ErrNoID)
	}
	return Nug{ID: id, Path: path, Body: strings.TrimSpace(body.String())}, true, nil
}

// unquote strips the surrounding whitespace and one layer of YAML scalar quotes
// ('x' or "x") from a frontmatter value.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if q := v[0]; (q == '\'' || q == '"') && v[len(v)-1] == q {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// ErrNoTool reports a friction nug whose body names no command to fingerprint.
var ErrNoTool = errors.New("friction nug names no tool")

// ErrNoID reports a friction nug whose frontmatter carries no id — a MUST field
// in mnemd's schema, and the key a downstream prompt opens the nug by.
var ErrNoID = errors.New("friction nug has no id")

// labelMax caps a signature label at a readable width; the full body stays in
// the nug, reachable by the id the label carries.
const labelMax = 80

// SignatureFromNug derives the known signature a friction nug records: the
// fingerprint of the command its `tool:` clause names, labelled by its
// `problem:` clause and its nug id.
//
// The fingerprint is built the way a live failed shell event's is, so a seeded
// signature and that event's FrictionText fingerprint byte-equal — the
// detector's only join. The clause is split with shellnorm exactly as
// internal/event splits a Bash tool call; the first segment's normalized
// command and raw text (truncated at event.DetailMax, the same cut the event
// builder applies) are joined as "cmd raw", the shape FrictionText yields for
// a shell failure; and Fingerprint masks the volatile parts. A clause shellnorm
// cannot segment takes the builder's own fallback shape, "sh <clause>".
//
// Only the first segment is kept: a nug's tool clause is a paraphrase dk wrote,
// and its first command is the one that failed. The join is only as literal as
// that clause — a tool clause written as prose (`bd update with a pipeline`)
// fingerprints to prose and will never meet a real event; the writer's side of
// the contract is to quote the command as it was run.
func SignatureFromNug(n Nug) (Signature, error) {
	tool, problem := clauses(n.Body)
	if tool == "" {
		return Signature{}, fmt.Errorf("%w: nug %s", ErrNoTool, n.ID)
	}
	text := "sh " + tool
	if segs, _ := shellnorm.Split(tool); len(segs) > 0 {
		text = segs[0].Cmd + " " + truncBytes(segs[0].Raw, event.DetailMax)
	}
	label := problem
	if label == "" {
		label = tool
	}
	return Signature{
		Fingerprint: Fingerprint(text),
		Label:       truncRunes(label, labelMax) + " (nug " + n.ID + ")",
	}, nil
}

// clauses splits a friction nug body into its tool and problem clauses. The
// body grammar is `tool: <command>. problem: <what went wrong>. resolution:
// <what fixed it>.` — the shape /wrap writes. A body without a `tool:` key
// yields no tool; a body without `problem:` yields no problem. Each clause runs
// to the next key or the end of the body, with the trailing sentence stop
// dropped.
func clauses(body string) (tool, problem string) {
	return clause(body, "tool:"), clause(body, "problem:")
}

var clauseKeys = []string{"tool:", "problem:", "resolution:"}

func clause(body, key string) string {
	_, rest, found := strings.Cut(body, key)
	if !found {
		return ""
	}
	end := len(rest)
	for _, k := range clauseKeys {
		if j := strings.Index(rest, k); j >= 0 && j < end {
			end = j
		}
	}
	s := strings.TrimSpace(rest[:end])
	s = strings.TrimRight(s, ".")
	return strings.Join(strings.Fields(s), " ")
}

// truncBytes cuts s to at most n bytes on a rune boundary — the event builder's
// trunc, replicated so the seeded fingerprint sees the same raw text an
// ingested event stores.
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// truncRunes cuts s to at most n runes, marking the cut.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
