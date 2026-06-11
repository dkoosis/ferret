# testdata — synthetic transcript corpus

Two ways to feed ferret without touching `~/.claude/projects`.

## Static fixtures — `testdata/corpus/`

Hand-crafted, in the `<project-slug>/<session>.jsonl` layout `transcript.Walk`
expects. Small enough to read; each session plants one shape:

| Session | Plants |
|---|---|
| `sess-routine`   | the canonical `Read → Edit → go_test → git_add → git_commit` routine, twice |
| `sess-friction`  | a retry chain (`go_test!` × 2 → pass) + a compound `cfail` (`go vet && go test`) |
| `sess-explore`   | a `Task` spawn, an `isMeta` line (dropped), and a subagent transcript |
| `sess-explore/subagents/agent-explore-01` | sidechain stream, keyed `(session, agent)` — never interleaves into the parent |

Point ferret at it:

```
ferret ingest -root testdata/corpus -data /tmp/fx
ferret graph  -data /tmp/fx -loops      # shows the friction bounce + subagent Grep⇄Read
```

These double as golden inputs — assert on miner output if you wire up e2e tests.

## Generator — `cmd/gen-corpus`

A standalone CLI that emits a **deterministic** corpus at any scale. Same
`-seed` + flags → byte-identical files (timestamps come from a fixed epoch plus
line sequence, never the wall clock).

```
go run ./cmd/gen-corpus -out /tmp/corpus -sessions 60 -seed 7
ferret ingest -root /tmp/corpus
# or just:
make corpus N=60
```

Archetype mix (weighted): `feature` routines dominate so n-grams/seqs rank them;
`friction` gives retry chains; `explore` gives noisy context; `withSubagent`
gives a sidechain file. At ≥60 sessions the default miner thresholds
(min-count=5, min-support=20) clear and every miner lights up.
